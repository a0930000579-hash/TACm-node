package node

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/crypto"
)

func testCfg(t *testing.T) *config.Config {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1
	return cfg
}

// insertGenesis 寫入一個空創世塊。
func insertGenesis(t *testing.T, n *Node) {
	t.Helper()
	now := time.Now().Unix()
	g := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: now,
	}
	if err := n.DB().InsertBlock(g, nil); err != nil {
		t.Fatalf("創世塊失敗: %v", err)
	}
}

// insertGenesisWithAlloc 寫入攜帶 alice 初始分配的創世塊。
func insertGenesisWithAlloc(t *testing.T, n *Node, alice string) {
	t.Helper()
	now := time.Now().Unix()
	genTx := chaindb.Transaction{
		TxHash: "genalloc", ToAddr: alice, Amount: "1000", Fee: "0", Ts: now,
	}
	g := &chaindb.Block{
		Height: 0, Hash: strings.Repeat("0", 64),
		Proposer: "genesis", Ts: now, TxCount: 1,
	}
	if err := n.DB().InsertBlock(g, []chaindb.Transaction{genTx}); err != nil {
		t.Fatalf("創世分配失敗: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("等待超時: " + msg)
}

func TestNodeProducesBlocks(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	defer func() { _ = n.Close() }()

	// 應持續出塊（即使無交易）。
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 3 },
		10*time.Second, "節點未持續出塊")

	height := n.DB().GetTipHeight()
	// M70：coinbase 瓜分寫入區塊——無「開機」礦工時節點 9% 歸出塊節點（節點獎勵），
	// 其餘 75%+16%＝91% 入獎勵池；節點地址進帳＝高度×0.9。
	bal, err := strconv.ParseFloat(n.DB().GetBalance(n.Address()), 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := float64(height) * 0.9; math.Abs(bal-want) > 1e-6 {
		t.Errorf("節點未開機節點獎勵=%g want %g (9%%×height=%d)", bal, want, height)
	}
	pool, err := strconv.ParseFloat(n.DB().GetBalance(chaindb.RewardPoolAddr), 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := float64(height) * 9.1; math.Abs(pool-want) > 1e-6 {
		t.Errorf("獎勵池=%g want %g (91%%×height=%d)", pool, want, height)
	}
}

// signedTx 構造一筆完整簽名交易 map。
func signedTx(t *testing.T, kp *crypto.KeyPair, to, amount, fee string, nonce int64) map[string]any {
	t.Helper()
	tx := map[string]any{
		"from": mustAddr(t, kp), "to": to, "amount": amount, "fee": fee,
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	tx["signature"] = sig
	return tx
}

func mustAddr(t *testing.T, kp *crypto.KeyPair) string {
	a, err := kp.Address()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestTransferEndToEnd(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bobKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()

	insertGenesisWithAlloc(t, n, alice)
	n.Start()
	defer func() { _ = n.Close() }()

	tx := signedTx(t, aliceKP, bob, "100", "1", 0)
	txHash, err := n.SubmitTransaction(tx)
	if err != nil {
		t.Fatalf("提交交易失敗: %v", err)
	}

	// 等待交易入塊。
	waitFor(t, func() bool {
		got, _ := n.DB().GetTransaction(txHash)
		return got != nil
	}, 10*time.Second, "交易未打包")

	// alice = 1000 - 100 - 1 = 899；bob = 100。
	assertNodeBalance(t, n, alice, 899)
	assertNodeBalance(t, n, bob, 100)

	// nonce 推進，舊 nonce 重放應被拒。
	old := signedTx(t, aliceKP, bob, "1", "0", 0)
	if _, err := n.SubmitTransaction(old); err == nil {
		t.Errorf("重放舊 nonce 應被拒")
	}
}

func TestNodeBFTFinality(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)
	n.Start()
	defer func() { _ = n.Close() }()

	// 等待出足夠塊。
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 5 },
		12*time.Second, "未出到 5 塊")

	// BFT 最終化高度應跟隨鏈頭（單機驗證人，每塊出後約 1s 內 finalize）。
	waitFor(t, func() bool { return n.FinalizedHeight() >= 3 },
		12*time.Second, "BFT 未跟隨最終化")

	info := n.GetFinalityInfo()
	if !info.HasBFT || info.ValidatorCount != 1 || info.QuorumPower != 1 {
		t.Errorf("最終性概況錯誤: %+v", info)
	}

	// 已最終化高度應能生成完備 precommit 證明。
	fh := n.FinalizedHeight()
	proof := n.GetFinalityProof(fh)
	if !proof.Finalized || len(proof.Votes) < 1 || proof.VotePower < 1 {
		t.Errorf("最終性證明錯誤: %+v", proof)
	}
}

func assertNodeBalance(t *testing.T, n *Node, addr string, want float64) {
	t.Helper()
	got, err := strconv.ParseFloat(n.DB().GetBalance(addr), 64)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("%s 餘額=%g want %g", addr, got, want)
	}
}

func TestRPC(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	insertGenesis(t, n)

	srv := httptest.NewServer(NewRPCServer(n).Handler())
	defer srv.Close()

	// GET /health
	health := getJSON(t, srv.URL+"/health")
	if health["status"] != "ok" {
		t.Errorf("health 異常: %v", health)
	}

	// GET /status
	status := getJSON(t, srv.URL+"/status")
	if status["node_id"] != "node1" {
		t.Errorf("status 異常: %v", status)
	}

	// GET /block/0
	block := getJSON(t, srv.URL+"/block/0")
	if block["height"] == nil {
		t.Errorf("block/0 異常: %v", block)
	}

	// GET /block/99 → 404
	resp, err := http.Get(srv.URL + "/block/99")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("block/99 應 404, got %d", resp.StatusCode)
	}

	// GET /wallet/new
	wallet := getJSON(t, srv.URL+"/wallet/new")
	if wallet["address"] == nil {
		t.Errorf("wallet/new 異常: %v", wallet)
	}

	// POST /tx/submit（合法簽名交易）
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	insertGenesisWithAlloc(t, n, alice)
	n.Start()
	defer func() { _ = n.Close() }()

	tx := signedTx(t, aliceKP, bob, "10", "0.5", 0)
	raw, _ := json.Marshal(tx)
	postResp, err := http.Post(srv.URL+"/tx/submit", "application/json",
		strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	json.NewDecoder(postResp.Body).Decode(&result)
	postResp.Body.Close()
	if result["ok"] != true {
		t.Errorf("RPC 提交交易失敗: %v", result)
	}
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}
