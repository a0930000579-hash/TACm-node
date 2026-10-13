package web

// M85-B：零依賴 WebSocket（RFC6455）＋以太坊式 eth_subscribe 訂閱。
// 支援：newHeads（鏈頂變化推送）、newPendingTransactions（交易池新增推送）、
// eth_unsubscribe。JSON-RPC 2.0 回應/通知格式，與 MetaMask/主流開發者工具相容。

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
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

	"tacm/internal/chaindb"
	"tacm/internal/exchange"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ---- frame opcodes ----
const (
	opText  = 1
	opClose = 8
	opPing  = 9
	opPong  = 10
)

// wsConn 代表一條 WebSocket 連接（單寫鎖保護並發寫入）。
type wsConn struct {
	conn       io.ReadWriteCloser
	mu         sync.Mutex
	subs       map[string]string     // 訂閱ID → 訂閱類型
	logsFilter map[string]*wsLogsSub // 訂閱ID → logs 過濾（僅 logs 訂閱）
}

// wsLogsSub 為 eth_subscribe logs 的過濾條件（address 任一、topics 層內 OR 跨層 AND）。
type wsLogsSub struct {
	Addresses []string
	Topics    [][]string
}

func (c *wsConn) writeText(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.conn, opText, payload, false)
}

// wsHub 管理所有活動連接與訂閱。
type wsHub struct {
	mu    sync.Mutex
	conns map[*wsConn]struct{}
}

func newWSHub() *wsHub { return &wsHub{conns: make(map[*wsConn]struct{})} }

func (h *wsHub) add(c *wsConn)    { h.mu.Lock(); h.conns[c] = struct{}{}; h.mu.Unlock() }
func (h *wsHub) remove(c *wsConn) { h.mu.Lock(); delete(h.conns, c); h.mu.Unlock() }

// hasMarketSub 返回是否有連接訂閱了指定市場（避免無訂閱時做無謂聚合）。
func (h *wsHub) hasMarketSub(market string) bool {
	key := "market:" + market
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		c.mu.Lock()
		for _, typ := range c.subs {
			if typ == key || typ == "market:*" {
				c.mu.Unlock()
				return true
			}
		}
		c.mu.Unlock()
	}
	return false
}

// broadcastMarket 推送 market_update 通知給訂閱了指定市場的連接（M114）。
func (h *wsHub) broadcastMarket(market, typ string, data any) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "market_update",
		"params": map[string]any{
			"subscription": "",
			"result":       map[string]any{"type": typ, "market": market, "data": data},
		},
	})
	if err != nil {
		return
	}
	key := "market:" + market
	h.mu.Lock()
	conns := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		want := false
		for _, t := range c.subs {
			if t == key || t == "market:*" {
				want = true
				break
			}
		}
		c.mu.Unlock()
		if want {
			_ = c.writeText(payload)
		}
	}
}

// broadcast 推送 eth_subscription 通知給訂閱了 subType 的連接。
func (h *wsHub) broadcast(subType string, params any) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "eth_subscription", "params": params,
	})
	if err != nil {
		return
	}
	h.mu.Lock()
	conns := make([]*wsConn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.mu.Lock()
		want := false
		for _, typ := range c.subs {
			if typ == subType {
				want = true
				break
			}
		}
		c.mu.Unlock()
		if want {
			_ = c.writeText(payload)
		}
	}
}

// handleWS 是 WebSocket 升級與訂閱處理入口（GET /ws）。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !isWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	accept := wsAccept(key)
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
	// 移除 bufio 殘留後改用原生 conn 讀寫（保持 RFC6455 幀位元組流）。
	c := &wsConn{conn: &brwConn{conn: conn, brw: brw}, subs: make(map[string]string), logsFilter: make(map[string]*wsLogsSub)}
	s.hub.add(c)
	defer s.hub.remove(c)

	// 讀迴圈：解析 JSON-RPC 方法。
	for {
		op, payload, err := readFrame(c.conn)
		if err != nil {
			return
		}
		switch op {
		case opClose:
			_ = c.writeText(nil) // 不發內容，直接關閉
			return
		case opPing:
			_ = writeFrame(c.conn, opPong, payload, false)
			continue
		case opText:
			if len(payload) == 0 {
				continue
			}
			s.handleWSMessage(c, payload)
		}
	}
}

// brwConn 包裝 Hijack 後的 bufio.ReadWriter，使讀寫都落在同一緩衝流。
type brwConn struct {
	conn io.ReadWriteCloser
	brw  *bufio.ReadWriter
}

func (b *brwConn) Read(p []byte) (int, error) { return b.brw.Read(p) }
func (b *brwConn) Write(p []byte) (int, error) {
	n, err := b.brw.Write(p)
	if err == nil {
		err = b.brw.Flush() // bufio.Writer 緩衝，必須立即送出
	}
	return n, err
}
func (b *brwConn) Close() error { return b.conn.Close() }

// handleWSMessage 處理單條 JSON-RPC 請求。
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
		case "logs":
			subID := fmt.Sprintf("0x%x", time.Now().UnixNano())
			sub := &wsLogsSub{}
			if len(req.Params) >= 2 {
				if fo, ok := req.Params[1].(map[string]any); ok {
					if a := fo["address"]; a != nil {
						switch av := a.(type) {
						case string:
							sub.Addresses = append(sub.Addresses, normWs0x(av))
						case []any:
							for _, item := range av {
								if s0, ok := item.(string); ok {
									sub.Addresses = append(sub.Addresses, normWs0x(s0))
								}
							}
						}
					}
					if ts := fo["topics"]; ts != nil {
						if arr, ok := ts.([]any); ok {
							sub.Topics = parseWsTopics(arr)
						}
					}
				}
			}
			c.mu.Lock()
			c.subs[subID] = "logs"
			c.logsFilter[subID] = sub
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
		delete(c.logsFilter, id)
		c.mu.Unlock()
		s.wsReplyResult(c, req.ID, ok)
	case "market_subscribe":
		// params: ["BTC/USDT"] — 訂閱交易所行情（M114：ticker 推送）。
		if len(req.Params) < 1 {
			s.wsReplyError(c, req.ID, -32602, "missing market")
			return
		}
		market, _ := req.Params[0].(string)
		if market == "" {
			s.wsReplyError(c, req.ID, -32602, "missing market")
			return
		}
		subID := fmt.Sprintf("mkt-%d", time.Now().UnixNano())
		c.mu.Lock()
		if market == "*" {
			c.subs[subID] = "market:*" // M114：萬用訂閱——接收所有市場行情
		} else {
			c.subs[subID] = "market:" + market
		}
		c.mu.Unlock()
		s.wsReplyResult(c, req.ID, subID)
	case "market_unsubscribe":
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

// wsPump 輪詢推送循環：newHeads（鏈頂變化）＋newPendingTransactions（交易池新增）。
func (s *Server) wsPump(stop <-chan struct{}) {
	lastHeight := int64(-1)
	lastPool := -1
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			st := s.ds.Status()
			h := st.Height
			if h != lastHeight {
				lastHeight = h
				if h >= 0 {
					head := map[string]any{
						"number":     "0x" + strconv.FormatInt(h, 16),
						"hash":       "",
						"proposer":   "",
						"difficulty": st.Difficulty,
						"ts":         0,
					}
					if bv, berr := s.ds.Block(h); berr == nil && bv != nil {
						head["hash"] = bv.Hash
						head["proposer"] = bv.Proposer
						head["ts"] = bv.Ts
					}
					s.hub.broadcast("newHeads", map[string]any{
						"subscription": "", "result": map[string]any{
							"type": "newHeads", "data": head,
						},
					})
					s.pushLogsSubs(lastHeight+1, h)
				}
			}
			ps := st.MempoolSize
			if ps != lastPool {
				lastPool = ps
				hashes, err := s.ds.PendingTxHashes(50)
				if err == nil && len(hashes) > 0 {
					s.hub.broadcast("newPendingTransactions", map[string]any{
						"subscription": "", "result": map[string]any{
							"type": "newPendingTransactions", "data": hashes,
						},
					})
				}
			}
		}
	}
}

// marketPump 交易所行情推送：每 500ms 對訂閱市場推一次 ticker（價格/24h 即時）。
func (s *Server) marketPump(stop <-chan struct{}) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			quotes, err := s.ds.ExchangeMarketTicks()
			if err != nil {
				continue
			}
			now := time.Now().Unix()
			for _, q := range quotes {
				if !s.hub.hasMarketSub(q.Market) {
					continue
				}
				s.hub.broadcastMarket(q.Market, "ticker", map[string]any{
					"price": q.Price, "chg24h": q.Chg24h,
					"ref": q.Ref, "live": q.Live, "fee_bps": q.FeeBps, "ts": now,
				})
				// M115：深度＋最新成交——與 ticker 同頻 500ms 推送，補齊即時行情閉環
				if q.Ref {
					// 參考市場：合成階梯深度（與前端 renderRefBook 一致，基於參考價 ±0.06%/檔）
					px := q.Price
					sb := make([]exchange.PriceLevel, 0, 14)
					sa := make([]exchange.PriceLevel, 0, 14)
					for si := 0; si < 14; si++ {
						sb = append(sb, exchange.PriceLevel{
							PriceStr: strconv.FormatFloat(px*(1-0.0006*float64(si+1)), 'f', 6, 64),
							QtyStr:   strconv.FormatFloat((12+float64(si)*8)*100, 'f', 4, 64),
						})
						sa = append(sa, exchange.PriceLevel{
							PriceStr: strconv.FormatFloat(px*(1+0.0006*float64(si+1)), 'f', 6, 64),
							QtyStr:   strconv.FormatFloat((12+float64(si)*8)*100, 'f', 4, 64),
						})
					}
					s.hub.broadcastMarket(q.Market, "depth", map[string]any{
						"bids": sb, "asks": sa, "ref": true, "ts": now,
					})
				} else {
					// 本地鏈上市場：真實訂單簿＋最新成交
					bids, asks, errB := s.ds.ExchangeOrderBook(q.Market, 12)
					if errB == nil && len(bids) > 0 && len(asks) > 0 {
						s.hub.broadcastMarket(q.Market, "depth", map[string]any{
							"bids": bids, "asks": asks, "ref": false, "ts": now,
						})
					}
					trades, errT := s.ds.ExchangeRecentTrades(q.Market, 8)
					if errT == nil && len(trades) > 0 {
						s.hub.broadcastMarket(q.Market, "trades", map[string]any{
							"trades": trades, "ts": now,
						})
					}
				}
			}
		}
	}
}

// ---- RFC6455 底層 ----

// wsAccept 計算 Sec-WebSocket-Accept。
func wsAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// writeFrame 寫一幀（server→client 不 mask）。
func writeFrame(w io.Writer, opcode byte, payload []byte, masked bool) error {
	b0 := 0x80 | (opcode & 0x0f)
	header := []byte{byte(b0)}
	l := len(payload)
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch {
	case l < 126:
		header = append(header, maskBit|byte(l))
	case l < 65536:
		header = append(header, maskBit|126, byte(l>>8), byte(l))
	default:
		header = append(header, maskBit|127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(l))
		header = append(header, b[:]...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if !masked {
		_, err := w.Write(payload)
		return err
	}
	mask := []byte{0x11, 0x22, 0x33, 0x44}
	if _, err := w.Write(mask); err != nil {
		return err
	}
	out := make([]byte, l)
	for i := 0; i < l; i++ {
		out[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.Write(out)
	return err
}

// readFrame 讀一幀（client→server 為 masked）。
func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	fin := hdr[0]&0x80 != 0
	op := hdr[0] & 0x0f
	if !fin {
		return 0, nil, errors.New("ws: fragmented frames unsupported")
	}
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7f)
	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	if length > 1<<20 {
		return 0, nil, errors.New("ws: frame too large")
	}
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return op, payload, nil
}


// normWs0x 統一 0x 前綴小寫（web 層 logs 過濾用）。
func normWs0x(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "0x") && !strings.HasPrefix(v, "0X") {
		v = "0x" + v
	}
	return strings.ToLower(v)
}

// parseWsTopics 解析 logs 訂閱 topics（層內 string 或陣列）。
func parseWsTopics(arr []any) [][]string {
	out := make([][]string, 0, len(arr))
	for _, layer := range arr {
		var vals []string
		switch lv := layer.(type) {
		case string:
			vals = append(vals, normWs0x(lv))
		case []any:
			for _, item := range lv {
				if s0, ok := item.(string); ok {
					vals = append(vals, normWs0x(s0))
				}
			}
		}
		out = append(out, vals)
	}
	return out
}

// pushLogsSubs 對所有 logs 訂閱推送 [from..to] 區間匹配日誌（M128）。
func (s *Server) pushLogsSubs(from, to int64) {
	s.hub.mu.Lock()
	conns := make([]*wsConn, 0, len(s.hub.conns))
	for c := range s.hub.conns {
		conns = append(conns, c)
	}
	s.hub.mu.Unlock()
	if from < 0 {
		from = 0
	}
	if to < from {
		return
	}
	for _, c := range conns {
		c.mu.Lock()
		var targets []string
		for id, typ := range c.subs {
			if typ == "logs" {
				targets = append(targets, id)
			}
		}
		c.mu.Unlock()
		for _, id := range targets {
			c.mu.Lock()
			sub := c.logsFilter[id]
			c.mu.Unlock()
			if sub == nil {
				continue
			}
			rows, err := s.ds.Logs(chaindb.LogFilter{
				FromBlock: from,
				ToBlock:   to,
				Addresses: sub.Addresses,
				Topics:    sub.Topics,
				Limit:     10000,
			})
			if err != nil || len(rows) == 0 {
				continue
			}
			items := make([]any, 0, len(rows))
			for i := range rows {
				items = append(items, map[string]any{
					"address":          rows[i].Address,
					"topics":           rows[i].Topics,
					"data":             rows[i].Data,
					"blockNumber":      "0x" + strconv.FormatInt(rows[i].BlockHeight, 16),
					"transactionHash":  "0x" + rows[i].TxHash,
					"transactionIndex": "0x" + strconv.FormatInt(int64(rows[i].TxIndex), 16),
					"logIndex":         "0x" + strconv.FormatInt(int64(rows[i].LogIndex), 16),
					"removed":          false,
				})
			}
			payload, perr := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "method": "eth_subscription",
				"params": map[string]any{
					"subscription": id,
					"result":       items,
				},
			})
			if perr == nil {
				_ = c.writeText(payload)
			}
		}
	}
}
