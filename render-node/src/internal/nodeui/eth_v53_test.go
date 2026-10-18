package nodeui

// v53-B1/C4/C5：節點端只讀 eth RPC、面板共識診斷＋對等節點、WS eth_subscribe 測試。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func startUIServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	n := startNode(t)
	s := New(n, "http://127.0.0.1:8080", "ui-test-node")
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func ethReadRPC(t *testing.T, ts *httptest.Server, method string, params ...any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/eth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("eth %s 失敗: %v", method, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("解析 eth %s 失敗: %v", method, err)
	}
	return out
}

func TestNodeUIEthReadonly(t *testing.T) {
	_, ts := startUIServer(t)

	// 出塊等待
	deadline := time.Now().Add(15 * time.Second)
	for {
		out := ethReadRPC(t, ts, "eth_blockNumber")
		if out["error"] == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("eth_blockNumber 持續失敗: %v", out["error"])
		}
		time.Sleep(300 * time.Millisecond)
	}

	// eth_blockNumber
	out := ethReadRPC(t, ts, "eth_blockNumber")
	if out["error"] != nil {
		t.Fatalf("eth_blockNumber 失敗: %v", out["error"])
	}
	num, _ := out["result"].(string)
	if !strings.HasPrefix(num, "0x") {
		t.Fatalf("blockNumber 格式錯誤: %v", out["result"])
	}

	// eth_chainId
	out = ethReadRPC(t, ts, "eth_chainId")
	if out["error"] != nil {
		t.Fatalf("eth_chainId 失敗: %v", out["error"])
	}

	// eth_getBlockByNumber latest
	out = ethReadRPC(t, ts, "eth_getBlockByNumber", "latest", false)
	if out["error"] != nil {
		t.Fatalf("eth_getBlockByNumber 失敗: %v", out["error"])
	}
	blk, ok := out["result"].(map[string]any)
	if !ok || blk["number"] == nil {
		t.Fatalf("區塊物件異常: %v", out["result"])
	}

	// eth_getBalance（節點地址）
	out = ethReadRPC(t, ts, "eth_getBalance", "0x0000000000000000000000000000000000000001", "latest")
	if out["error"] != nil {
		t.Fatalf("eth_getBalance 失敗: %v", out["error"])
	}

	// admin_peers（無 P2P → 空陣列）
	out = ethReadRPC(t, ts, "admin_peers")
	if out["error"] != nil {
		t.Fatalf("admin_peers 失敗: %v", out["error"])
	}
	if _, ok := out["result"].([]any); !ok {
		t.Fatalf("admin_peers 非陣列: %T", out["result"])
	}

	// debug_getRawBlock
	out = ethReadRPC(t, ts, "debug_getRawBlock", "latest")
	if out["error"] != nil {
		t.Fatalf("debug_getRawBlock 失敗: %v", out["error"])
	}

	// batch 支援
	body, _ := json.Marshal([]map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber", "params": []any{}},
		{"jsonrpc": "2.0", "id": 2, "method": "eth_chainId", "params": []any{}},
	})
	resp, err := http.Post(ts.URL+"/eth", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var arr []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		t.Fatalf("batch 解析失敗: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("batch 應回 2 筆: %d", len(arr))
	}
}

func TestNodeUIPanelConsensusAndPeers(t *testing.T) {
	_, ts := startUIServer(t)
	resp, err := http.Get(ts.URL + "/api/node/panel")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var d map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatal(err)
	}
	// 共識診斷欄位
	if d["finalized_height"] == nil {
		t.Fatal("缺少 finalized_height")
	}
	if d["validator_total"] == nil {
		t.Fatal("缺少 validator_total")
	}
	if d["peers"] == nil {
		t.Fatal("缺少 peers")
	}
	if _, ok := d["peers"].([]any); !ok {
		t.Fatalf("peers 非陣列: %T", d["peers"])
	}
}

// TestNodeUIWSSubscribe 以手寫 WS client 驗證 eth_subscribe（newHeads）。
func TestNodeUIWSSubscribe(t *testing.T) {
	s, ts := startUIServer(t)
	_ = s
	// 直接連底層 listener 的 WS（httptest server URL 轉 ws://）
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, err := dialWS(wsURL, t)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// eth_subscribe newHeads
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_subscribe",
		"params": []any{"newHeads"},
	})
	if err := writeText(conn, req); err != nil {
		t.Fatal(err)
	}
	// 讀 subscribe 確認
	op, payload, err := readWSFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	if op != opText {
		t.Fatalf("subscribe 確認非 text: %d", op)
	}
	var ack struct {
		ID     int    `json:"id"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(payload, &ack); err != nil || ack.Result == "" {
		t.Fatalf("subscribe ack 異常: %v %s", err, payload)
	}

	// 等待新區塊通知（節點持續出塊，1s tick 內應收到 newHeads）
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		op, payload, err := readWSFrame(conn)
		if err != nil {
			continue
		}
		if op != opText {
			continue
		}
		var notif struct {
			Method string `json:"method"`
			Params struct {
				Result struct {
					Type string `json:"type"`
				} `json:"result"`
			} `json:"params"`
		}
		if json.Unmarshal(payload, &notif) == nil && notif.Method == "eth_subscription" &&
			notif.Params.Result.Type == "newHeads" {
			return
		}
	}
	t.Fatal("20s 內未收到 newHeads 通知")
}

// ————— 測試用最小 WS client —————

func dialWS(url string, t *testing.T) (net.Conn, error) {
	t.Helper()
	// 分離 host（含 port）與 path。
	u := strings.TrimPrefix(url, "ws://")
	hostPath := strings.TrimPrefix(u, "wss://")
	host := hostPath
	path := "/"
	if i := strings.Index(hostPath, "/"); i >= 0 {
		host = hostPath[:i]
		path = hostPath[i:]
	}
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, err
	}
	key := "dGhlIHNhbXBsZSBub25jZQ==" // RFC6455 範例 key
	handshake := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(handshake)); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || !strings.Contains(line, "101") {
		conn.Close()
		return nil, err
	}
	// 讀完剩餘 header
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	// 包裝 bufio 讀寫（後續幀讀寫走同緩衝）
	return &testWSConn{conn: conn, br: br}, nil
}

type testWSConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func (c *testWSConn) Read(p []byte) (int, error)  { return c.br.Read(p) }
func (c *testWSConn) Write(p []byte) (int, error) { return c.conn.Write(p) }
func (c *testWSConn) Close() error                { return c.conn.Close() }
func (c *testWSConn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}
func (c *testWSConn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}
func (c *testWSConn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}
func (c *testWSConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *testWSConn) RemoteAddr() net.Addr               { return c.conn.RemoteAddr() }

func writeText(c net.Conn, payload []byte) error {
	// 客戶端幀：masked（RFC6455 要求）
	head := []byte{0x81, 0x80 | byte(len(payload))}
	mask := []byte{0x12, 0x34, 0x56, 0x78}
	body := make([]byte, len(payload))
	for i := range payload {
		body[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.Write(append(append(head, mask...), body...)); err != nil {
		return err
	}
	return nil
}
