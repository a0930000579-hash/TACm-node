package web

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/node"
)

// TestWSHandshakeAccept RFC6455 §1.3 官方範例向量。
func TestWSHandshakeAccept(t *testing.T) {
	got := wsAccept("dGhlIHNhbXBsZSBub25jZQ==")
	want := "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got != want {
		t.Fatalf("accept = %q, want %q", got, want)
	}
}

// TestWSFrameRoundTrip 編碼（unmasked）＋解碼（masked）往返。
func TestWSFrameRoundTrip(t *testing.T) {
	// server→client unmasked
	var buf bytes.Buffer
	if err := writeFrame(&buf, opText, []byte(`{"ok":true}`), false); err != nil {
		t.Fatal(err)
	}
	op, payload, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if op != opText || string(payload) != `{"ok":true}` {
		t.Fatalf("roundtrip: op=%d payload=%s", op, payload)
	}

	// client→server masked（RFC 6455 masked bit + mask key）
	var buf2 bytes.Buffer
	raw := []byte(`{"method":"eth_subscribe"}`)
	// 手工構建 masked frame：b0=0x81, b1=0x80|len, mask=4 bytes
	buf2.Write([]byte{0x81, 0x80 | byte(len(raw)), 0x11, 0x22, 0x33, 0x44})
	for i, c := range raw {
		buf2.Write([]byte{c ^ []byte{0x11, 0x22, 0x33, 0x44}[i%4]})
	}
	op2, payload2, err := readFrame(&buf2)
	if err != nil {
		t.Fatal(err)
	}
	if op2 != opText || string(payload2) != string(raw) {
		t.Fatalf("masked: op=%d payload=%s", op2, payload2)
	}
}

// wsTestClient 最小 WebSocket 客戶端（用本包 frame 函數）。
type wsTestClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialWS(t *testing.T, url string) *wsTestClient {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("test-key-1234567890"))
	req := "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil || !strings.Contains(line, "101") {
		t.Fatalf("handshake: %q err=%v", line, err)
	}
	// 讀完剩餘 header
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(l) == "" {
			break
		}
	}
	return &wsTestClient{conn: conn, r: r}
}

func (c *wsTestClient) send(t *testing.T, payload string) {
	t.Helper()
	raw := []byte(payload)
	mask := []byte{0x11, 0x22, 0x33, 0x44}
	hdr := []byte{0x81, 0x80 | byte(len(raw))}
	hdr = append(hdr, mask...)
	if _, err := c.conn.Write(hdr); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i++ {
		out[i] = raw[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(out); err != nil {
		t.Fatal(err)
	}
}

func (c *wsTestClient) recv(t *testing.T, timeout time.Duration) (byte, []byte) {
	t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(timeout))
	op, payload, err := readFrame(c.r)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	return op, payload
}

// TestWSEthSubscribeE2E 真實節點：訂閱 newHeads → 節點出塊 → 收到通知。
func TestWSEthSubscribeE2E(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1

	n, err := node.New(cfg, "wse2e", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: time.Now().Unix(),
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatal(err)
	}
	n.Start()

	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatal(err)
	}
	srv.StartWS()
	defer srv.CloseWS()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := dialWS(t, ts.URL)
	defer c.conn.Close()

	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`)
	_, sub := c.recv(t, 5*time.Second)
	var sr struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(sub, &sr); err != nil {
		t.Fatalf("subscribe resp: %v (%s)", err, sub)
	}
	if sr.Result == "" || sr.ID != 1 {
		t.Fatalf("subscribe resp: %s", sub)
	}

	// 節點持續出塊 → 應在數秒內收到 newHeads 通知。
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		_, msg := c.recv(t, 4*time.Second)
		var nt struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  struct {
				Result struct {
					Type string         `json:"type"`
					Data map[string]any `json:"data"`
				} `json:"result"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &nt); err != nil {
			t.Fatalf("notify: %v (%s)", err, msg)
		}
		if nt.Method == "eth_subscription" && nt.Params.Result.Type == "newHeads" {
			if nt.Params.Result.Data["number"] == "" {
				t.Fatalf("newHeads missing number: %s", msg)
			}
			return // 收到新頭部通知，測試通過
		}
	}
	t.Fatal("未在超時內收到 newHeads 通知")
}

// TestWSEthUnsubscribe 訂閱→取消→回應 true。
func TestWSEthUnsubscribe(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 60 // 不出塊，專測訂閱管理

	n, err := node.New(cfg, "wsu2", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: time.Now().Unix(),
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatal(err)
	}
	n.Start()

	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatal(err)
	}
	srv.StartWS()
	defer srv.CloseWS()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := dialWS(t, ts.URL)
	defer c.conn.Close()
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`)
	_, sub := c.recv(t, 5*time.Second)
	var sr struct {
		Result string `json:"result"`
	}
	_ = json.Unmarshal(sub, &sr)

	c.send(t, `{"jsonrpc":"2.0","id":2,"method":"eth_unsubscribe","params":["`+sr.Result+`"]}`)
	_, un := c.recv(t, 5*time.Second)
	var ur struct {
		ID     int  `json:"id"`
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(un, &ur); err != nil {
		t.Fatalf("unsubscribe: %v (%s)", err, un)
	}
	if ur.ID != 2 || !ur.Result {
		t.Fatalf("unsubscribe resp: %s", un)
	}
}

var _ = http.MethodGet

// TestWSMarketSubscribe 訂閱交易所行情 → subscription id＋market_update ticker 推送（M114）。
func TestWSMarketSubscribe(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 60 // 不出塊，專測行情推送

	n, err := node.New(cfg, "wsm1", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: time.Now().Unix(),
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatal(err)
	}
	n.Start()

	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatal(err)
	}
	srv.StartWS()
	defer srv.CloseWS()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := dialWS(t, ts.URL)
	defer c.conn.Close()
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"market_subscribe","params":["BTC/USDT"]}`)
	_, sub := c.recv(t, 5*time.Second)
	var sr struct {
		ID     int    `json:"id"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(sub, &sr); err != nil {
		t.Fatalf("subscribe resp: %v (%s)", err, sub)
	}
	if sr.Result == "" || sr.ID != 1 {
		t.Fatalf("subscribe resp: %s", sub)
	}

	// 500ms 推送週期 → 3 秒內應收到 BTC/USDT ticker。
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		_, msg := c.recv(t, 2*time.Second)
		var nt struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  struct {
				Result struct {
					Type   string `json:"type"`
					Market string `json:"market"`
					Data   struct {
						Price  float64 `json:"price"`
						Chg24h float64 `json:"chg24h"`
					} `json:"data"`
				} `json:"result"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &nt); err != nil {
			t.Fatalf("notify: %v (%s)", err, msg)
		}
		if nt.Method == "market_update" && nt.Params.Result.Type == "ticker" && nt.Params.Result.Market == "BTC/USDT" {
			if nt.Params.Result.Data.Price <= 0 {
				t.Fatalf("ticker price<=0: %s", msg)
			}
			return // 收到行情推送，測試通過
		}
	}
	t.Fatal("未在超時內收到 market_update ticker")
}

// TestWSMarketUnsubscribe 訂閱→取消→不再收到該市場推送。
func TestWSMarketUnsubscribe(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 60

	n, err := node.New(cfg, "wsm2", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: time.Now().Unix(),
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatal(err)
	}
	n.Start()

	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatal(err)
	}
	srv.StartWS()
	defer srv.CloseWS()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := dialWS(t, ts.URL)
	defer c.conn.Close()
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"market_subscribe","params":["BTC/USDT"]}`)
	_, sub := c.recv(t, 5*time.Second)
	var sr struct{ Result string `json:"result"` }
	_ = json.Unmarshal(sub, &sr)
	if sr.Result == "" {
		t.Fatalf("subscribe resp: %s", sub)
	}

	c.send(t, `{"jsonrpc":"2.0","id":2,"method":"market_unsubscribe","params":["`+sr.Result+`"]}`)
	_, un := c.recv(t, 5*time.Second)
	var ur struct {
		ID     int  `json:"id"`
		Result bool `json:"result"`
	}
	if err := json.Unmarshal(un, &ur); err != nil {
		t.Fatalf("unsub resp: %v (%s)", err, un)
	}
	if !ur.Result || ur.ID != 2 {
		t.Fatalf("unsub resp: %s", un)
	}

	// 取消後 1.5 秒內不應再收到 ticker（讀超時＝無推送，測試通過）。
	_ = c.conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	op2, extra, err2 := readFrame(c.r)
	if err2 == nil && op2 == opText {
		var nt struct{ Method string `json:"method"` }
		_ = json.Unmarshal(extra, &nt)
		if nt.Method == "market_update" {
			t.Fatalf("取消後仍收到 market_update: %s", extra)
		}
	}
}

// TestWSLogsSubscribeE2E 真實節點：訂閱 logs → 鏈上產生事件日誌 → 收到 eth_subscription 通知。
func TestWSLogsSubscribeE2E(t *testing.T) {
	cfg := config.Default()
	n, err := node.New(cfg, "wslogse2e", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	genesis := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: time.Now().Unix(),
	}
	if err := n.DB().InsertBlock(genesis, nil); err != nil {
		t.Fatal(err)
	}
	n.Start()
	srv, err := New(NewNodeDataSource(n))
	if err != nil {
		t.Fatal(err)
	}
	srv.StartWS()
	defer srv.CloseWS()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := dialWS(t, ts.URL)
	defer c.conn.Close()

	// 訂閱 logs（address 過濾）。
	c.send(t, `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs",{"address":"0xabc123abc123abc123abc123abc123abc123abc1"}]}`)
	_, sub := c.recv(t, 5*time.Second)
	var sr struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(sub, &sr); err != nil {
		t.Fatalf("logs subscribe resp: %v (%s)", err, sub)
	}
	if sr.Result == "" {
		t.Fatalf("logs subscribe resp: %s", sub)
	}

	// 模擬鏈上事件日誌（Transfer(address,address,uint256) topic0，高度 1）。
	rows := []chaindb.LogRow{{
		Address: "0xabc123abc123abc123abc123abc123abc123abc1",
		Topics:  []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef", "0x" + strings.Repeat("0", 64), "0x" + strings.Repeat("1", 64)},
		Data:    "0x" + strings.Repeat("00", 32),
	}}
	if err := n.DB().InsertLogs("0x"+strings.Repeat("ab", 32), 1, 0, rows); err != nil {
		t.Fatal(err)
	}
	// 模擬 wsPump 在新區塊觸發推送（測試 wsPump 的 logs 推送邏輯本身）。
	srv.pushLogsSubs(1, 1)

	// pushLogsSubs 推送後應在數秒內收到 logs 通知。
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		_, msg := c.recv(t, 4*time.Second)
		var nt struct {
			JSONRPC string `json:"jsonrpc"`
			Method  string `json:"method"`
			Params  struct {
				Subscription string          `json:"subscription"`
				Result       json.RawMessage `json:"result"`
			} `json:"params"`
		}
		if err := json.Unmarshal(msg, &nt); err != nil {
			t.Fatalf("logs notify: %v (%s)", err, msg)
		}
		if nt.Method == "eth_subscription" && nt.Params.Subscription == sr.Result {
			var logs []map[string]any
			if err := json.Unmarshal(nt.Params.Result, &logs); err == nil && len(logs) > 0 {
				return // 收到 logs 通知，測試通過
			}
		}
	}
	t.Fatal("未在超時內收到 logs 通知")
}
