package node

// reward_withdraw_test.go — M77：節點獎勵提取端點 e2e。
// 驗證：創世節點餘額 → 本機 reward-withdraw 上鏈 → 收款方餘額增加；
// 非本機來源 403、壞參數 400。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
)

// newRewardHost 建立節點＋創世（節點金鑰地址 100 TACm，模擬節點獎勵累計）＋RPC host。
func newRewardHost(t *testing.T, blockTime int) (*Node, *httptest.Server) {
	t.Helper()
	cfg := testCfg(t)
	cfg.BlockTime = blockTime
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	nodeAddr, err := n.keypair.Address()
	if err != nil {
		t.Fatalf("node address: %v", err)
	}
	now := time.Now().Unix()
	genTx := chaindb.Transaction{
		TxHash: "gen-node-reward", ToAddr: nodeAddr,
		Amount: "100", Fee: "0", Ts: now, Memo: "genesis:node_reward_test",
	}
	g := &chaindb.Block{Height: 0, Hash: strings.Repeat("0", 64), Proposer: "genesis", Ts: now, TxCount: 1}
	if err := n.DB().InsertBlock(g, []chaindb.Transaction{genTx}); err != nil {
		t.Fatalf("創世失敗: %v", err)
	}
	n.Start()
	srv := NewRPCServer(n)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = n.Close() })
	return n, ts
}

// TestNodeRewardWithdrawEndToEnd：本機提取 → 入塊 → 收款方到帳；非本機 403。
func TestNodeRewardWithdrawEndToEnd(t *testing.T) {
	n, ts := newRewardHost(t, 1)
	nodeAddr, err := n.keypair.Address()
	if err != nil {
		t.Fatal(err)
	}
	to := testAddr(t)

	// 等鏈頂 ≥1（節點出塊，節點地址累計 coinbase:node 9%）。
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 1 }, 10*time.Second, "鏈高未到 1")

	// 本機提取 5 TACm。
	body, _ := json.Marshal(map[string]any{"to": to, "amount": "5"})
	resp, err := http.Post(ts.URL+"/api/node/reward-withdraw", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var out struct {
		Ok     bool   `json:"ok"`
		TxHash string `json:"tx_hash"`
		From   string `json:"from"`
		To     string `json:"to"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if !out.Ok || out.TxHash == "" {
		t.Fatalf("withdraw 未成功: %+v", out)
	}
	if out.From != nodeAddr || out.To != to {
		t.Fatalf("from/to 不匹配: %+v", out)
	}

	// 等出塊後收款方餘額 = 5。
	waitFor(t, func() bool {
		return n.DB().GetBalance(to) == "5" || strings.HasPrefix(n.DB().GetBalance(to), "5.")
	}, 10*time.Second, "收款方餘額未到帳")

	bal := n.DB().GetBalance(to)
	f, ferr := strconv.ParseFloat(bal, 64)
	if ferr != nil || f < 5 || f >= 6 {
		t.Fatalf("收款方餘額異常: %s (err=%v)", bal, ferr)
	}

	// 非本機來源 → 403。
	req, _ := http.NewRequest(http.MethodPost, "/api/node/reward-withdraw", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.9:55123"
	rec := httptest.NewRecorder()
	NewRPCServer(n).Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非本機應 403，got %d", rec.Code)
	}

	// 壞 to → 400。
	badBody, _ := json.Marshal(map[string]any{"to": "not-an-address", "amount": "1"})
	req2, _ := http.NewRequest(http.MethodPost, "/api/node/reward-withdraw", bytes.NewReader(badBody))
	req2.RemoteAddr = "127.0.0.1:12345"
	rec2 := httptest.NewRecorder()
	NewRPCServer(n).Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("壞 to 應 400，got %d", rec2.Code)
	}
}
