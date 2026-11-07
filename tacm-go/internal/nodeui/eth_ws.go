package nodeui

// v53-C5：節點端 WebSocket eth_subscribe（零依賴 RFC6455，與主鏈 M85-B 同構）。
// 支援 newHeads（鏈頂變化）與 newPendingTransactions（交易池新增）；eth_unsubscribe
// 取消訂閱。MetaMask/主流開發者工具可直接連 ws://<node>/ws 使用。
//
// 本文件自含最小 RFC6455 frame 處理（讀 text/ping/close、寫 text/pong），
// 不引入第三方依賴——與內部包的差異是 nodeui 為獨立 binary（cmd/nodeweb）。

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

// wsConn 單一 WebSocket 連線（訂閱集合＋寫鎖）。
type wsConn struct {
	conn io.ReadWriteCloser
	mu   sync.Mutex
	subs map[string]string // subID → 訂閱類型（newHeads / newPendingTransactions）
}

func (c *wsConn) writeText(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeWSFrame(c.conn, opText, payload, false)
}

// wsHub 連線集合＋廣播。
type wsHub struct {
	mu    sync.Mutex
	conns map[*wsConn]struct{}
}

func newWSHub() *wsHub { return &wsHub{conns: make(map[*wsConn]struct{})} }

func (h *wsHub) add(c *wsConn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *wsHub) remove(c *wsConn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// broadcast 推送 eth_subscription 通知給訂閱了 subType 的連線。
func (h *wsHub) broadcast(subType string, params any) {
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "eth_subscription",
		"params": params,
	})
	h.mu.Lock()
	conns := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		active := false
		for _, t := range c.subs {
			if t == subType {
				active = true
				break
			}
		}
		c.mu.Unlock()
		if !active {
			continue
		}
		if err := c.writeText(payload); err != nil {
			h.remove(c)
		}
	}
}

// handleWS GET /ws — WebSocket 升級＋eth_subscribe 讀迴圈。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !isWSUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	accept := wsAcceptKey(key)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		return
	}
	c := &wsConn{conn: &wsBrwConn{conn: conn, brw: brw}, subs: make(map[string]string)}
	s.hub.add(c)
	defer s.hub.remove(c)

	for {
		op, payload, err := readWSFrame(c.conn)
		if err != nil {
			return
		}
		switch op {
		case opClose:
			return
		case opPing:
			_ = writeWSFrame(c.conn, opPong, payload, false)
			continue
		case opText:
			if len(payload) == 0 {
				continue
			}
			s.handleWSMessage(c, payload)
		}
	}
}

// wsBrwConn 包裝 Hijack 後的 bufio.ReadWriter。
type wsBrwConn struct {
	conn io.ReadWriteCloser
	brw  *bufio.ReadWriter
}

func (b *wsBrwConn) Read(p []byte) (int, error) { return b.brw.Read(p) }

// Write 寫入緩衝後立即 Flush——Hijack 的 bufio.Writer 不會自行排空，
// 若不 Flush，響應/廣播會永久卡在緩衝（v53-C5 修復）。
func (b *wsBrwConn) Write(p []byte) (int, error) {
	n, err := b.brw.Write(p)
	if err == nil && n == len(p) {
		if ferr := b.brw.Flush(); ferr != nil {
			return n, ferr
		}
	}
	return n, err
}
func (b *wsBrwConn) Close() error                { return b.conn.Close() }

// handleWSMessage 處理單幀 JSON-RPC 訊息。
func (s *Server) handleWSMessage(c *wsConn, payload []byte) {
	var req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Method  string `json:"method"`
		Params  []any  `json:"params"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		s.wsReplyError(c, nil, -32700, "parse error")
		return
	}
	switch req.Method {
	case "eth_subscribe":
		if len(req.Params) < 1 {
			s.wsReplyError(c, req.ID, -32602, "missing subscription type")
			return
		}
		typ, _ := req.Params[0].(string)
		switch typ {
		case "newHeads", "newPendingTransactions":
			subID := fmt.Sprintf("0x%x", time.Now().UnixNano())
			c.mu.Lock()
			c.subs[subID] = typ
			c.mu.Unlock()
			s.wsReplyResult(c, req.ID, subID)
		default:
			s.wsReplyError(c, req.ID, -32602, "unsupported subscription: "+typ)
		}
	case "eth_unsubscribe":
		if len(req.Params) < 1 {
			s.wsReplyError(c, req.ID, -32602, "missing subscription id")
			return
		}
		id, _ := req.Params[0].(string)
		c.mu.Lock()
		_, ok := c.subs[id]
		delete(c.subs, id)
		c.mu.Unlock()
		s.wsReplyResult(c, req.ID, ok)
	default:
		s.wsReplyError(c, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) wsReplyResult(c *wsConn, id any, result any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	_ = c.writeText(payload)
}

func (s *Server) wsReplyError(c *wsConn, id any, code int, msg string) {
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
	_ = c.writeText(payload)
}

// wsPump 輪詢推送：newHeads（鏈頂變化）＋newPendingTransactions（交易池新增）。
func (s *Server) wsPump(stop <-chan struct{}) {
	lastHeight := int64(-1)
	lastPool := int64(-1)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			h := s.n.DB().GetTipHeight()
			if h != lastHeight {
				lastHeight = h
				if h >= 0 {
					head := map[string]any{
						"number":     "0x" + strconv.FormatInt(h, 16),
						"hash":       "", "proposer": "", "difficulty": 0, "ts": int64(0),
					}
					if bv, berr := s.n.DB().GetBlock(h); berr == nil && bv != nil {
						head["hash"] = "0x" + bv.Hash
						head["proposer"] = bv.Proposer
						head["difficulty"] = bv.Difficulty
						head["ts"] = bv.Ts
					}
					s.hub.broadcast("newHeads", map[string]any{
						"subscription": "", "result": map[string]any{
							"type": "newHeads", "data": head,
						},
					})
				}
			}
			pool := s.n.DB().MempoolSize()
			if pool != lastPool && pool > 0 {
				lastPool = pool
				txs, _ := s.n.DB().MempoolTxs()
				for _, tx := range txs {
					s.hub.broadcast("newPendingTransactions", map[string]any{
						"subscription": "", "result": map[string]any{
							"type": "newPendingTransactions", "data": "0x" + tx.TxHash,
						},
					})
				}
			}
		}
	}
}

// ————— RFC6455 最小 frame 處理 —————

// readWSFrame 讀一幀：回 (opcode, payload, error)；masked 客戶端幀自動解遮罩。
func readWSFrame(r io.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := hdr[0] & 0x0f
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7f)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	} else if length == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > 1<<20 { // 1MB 幀上限防灌水
		return 0, nil, errors.New("ws: frame too large")
	}
	// RFC6455：mask key 在 payload 之前。
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return op, payload, nil
}

// writeWSFrame 寫一幀（服務器→客戶端：不遮罩）。
func writeWSFrame(w io.Writer, op byte, payload []byte, masked bool) error {
	var head []byte
	head = append(head, 0x80|op)
	length := len(payload)
	switch {
	case length < 126:
		head = append(head, byte(length))
	case length <= 0xffff:
		head = append(head, 126, byte(length>>8), byte(length))
	default:
		head = append(head, 127, 0, 0, 0, 0, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	if masked {
		head[1] |= 0x80
		head = append(head, 0, 0, 0, 0)
	}
	if _, err := w.Write(head); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

// wsAcceptKey 計算 Sec-WebSocket-Accept（RFC6455）。
func wsAcceptKey(key string) string {
	// SHA-1 + GUID（節點 UI 為獨立 binary，避免 import 命名衝突，直接在此計算）。
	return wsSHA1Base64(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
}

func wsSHA1Base64(s string) string {
	// 以標準庫 crypto/sha1 + encoding/base64 計算（見 ws_sha1.go 以保持本檔簡潔）。
	return wsSHA1Of(s)
}

// isWSUpgrade 判斷是否為 WebSocket 升級請求。
func isWSUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}
