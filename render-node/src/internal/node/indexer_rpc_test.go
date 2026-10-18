package node

// M155 索引 API e2e：造鏈（創世＋交易）→ 驗證 stats/blocks/block/tx/txs/address 端點。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
	"strings"
)

func newIndexerTestServer(t *testing.T) (*httptest.Server, *Node, *crypto.KeyPair) {
	t.Helper()
	n, err := New(testCfg(t), "idx1", 3)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	insertGenesisWithAlloc(t, n, alice)
	n.Start()

	// 一筆真實交易（alice→節點位址，nonce0）供查詢。
	nodeAddr, _ := n.keypair.Address()
	tx := signedTxWithMemo(t, aliceKP, nodeAddr, "5", "1", 0, "")
	if _, err := n.SubmitTransaction(tx); err != nil {
		t.Fatalf("提交交易失敗: %v", err)
	}
	// 等打包（區塊至少含 1 筆交易）。
	waitFor(t, func() bool {
		b, err := n.db.GetLatestBlock()
		if err != nil || b == nil {
			return false
		}
		return b.Height >= 1
	}, 15*time.Second, "交易未打包")
	return httptest.NewServer(NewRPCServer(n).Handler()), n, aliceKP
}

func idxGet(t *testing.T, srv *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s 失敗: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s 解析失敗: %v", path, err)
	}
	return out
}

func TestIndexerStats(t *testing.T) {
	srv, _, _ := newIndexerTestServer(t)
	out := idxGet(t, srv, "/api/v1/stats")
	if out["ok"] != true {
		t.Fatalf("stats 應 ok，got %+v", out)
	}
	if out["height"].(float64) < 1 {
		t.Fatalf("height 應 ≥1，got %+v", out)
	}
	if out["block_count"].(float64) < 1 || out["tx_count"].(float64) < 1 {
		t.Fatalf("block/tx count 應 ≥1，got %+v", out)
	}
}

func TestIndexerBlocksAndBlock(t *testing.T) {
	srv, n, _ := newIndexerTestServer(t)
	out := idxGet(t, srv, "/api/v1/blocks?limit=5")
	if out["ok"] != true {
		t.Fatalf("blocks 應 ok，got %+v", out)
	}
	blocks, _ := out["blocks"].([]any)
	if len(blocks) == 0 {
		t.Fatal("blocks 應非空")
	}
	first := blocks[0].(map[string]any)
	h := int64(first["height"].(float64))
	// 按高度查。
	b := idxGet(t, srv, fmt.Sprintf("/api/v1/block/%d", h))
	if b["ok"] != true {
		t.Fatalf("block by height 應 ok，got %+v", b)
	}
	// 按哈希查。
	hash := first["hash"].(string)
	b2 := idxGet(t, srv, "/api/v1/block/"+hash)
	if b2["ok"] != true {
		t.Fatalf("block by hash 應 ok，got %+v", b2)
	}
	// 不存在 → 404。
	resp, err := srv.Client().Get(srv.URL + "/api/v1/block/999999")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在區塊應 404，got %d", resp.StatusCode)
	}
	_ = n
}

func TestIndexerTxAndTxs(t *testing.T) {
	srv, n, aliceKP := newIndexerTestServer(t)
	alice, _ := aliceKP.Address()
	// 從區塊明細取交易（blocks 列表僅 block 欄位，txs 在 block 端點）。
	out := idxGet(t, srv, "/api/v1/blocks?limit=1")
	blocks, _ := out["blocks"].([]any)
	blk := blocks[0].(map[string]any)
	b := idxGet(t, srv, fmt.Sprintf("/api/v1/block/%d", int64(blk["height"].(float64))))
	btxs, _ := b["txs"].([]any)
	if len(btxs) == 0 {
		t.Fatal("block 明細應含交易")
	}
	tx0 := btxs[0].(map[string]any)
	hash := tx0["tx_hash"].(string)
	// 單筆交易。
	tx := idxGet(t, srv, "/api/v1/tx/"+hash)
	if tx["ok"] != true {
		t.Fatalf("tx 應 ok，got %+v", tx)
	}
	// 地址交易歷史（alice 至少 1 筆）。
	hist := idxGet(t, srv, "/api/v1/txs?address="+alice)
	if hist["ok"] != true || hist["total"].(float64) < 1 {
		t.Fatalf("地址交易歷史應 ≥1，got %+v", hist)
	}
	// 非法哈希 → 400。
	resp, err := srv.Client().Get(srv.URL + "/api/v1/tx/zzz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法哈希應 400，got %d", resp.StatusCode)
	}
	_ = n
}

func TestIndexerAddress(t *testing.T) {
	srv, _, aliceKP := newIndexerTestServer(t)
	alice, _ := aliceKP.Address()
	out := idxGet(t, srv, "/api/v1/address/"+alice)
	if out["ok"] != true {
		t.Fatalf("address 應 ok，got %+v", out)
	}
	acct := out["account"].(map[string]any)
	bal, _ := acct["balance"].(string)
	if bal == "" {
		t.Fatalf("balance 應非空，got %+v", acct)
	}
	if out["tx_count"].(float64) < 1 {
		t.Fatalf("tx_count 應 ≥1，got %+v", out)
	}
	// 非法地址 → 400。
	resp, err := srv.Client().Get(srv.URL + "/api/v1/address/zzz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法地址應 400，got %d", resp.StatusCode)
	}
}

func TestIndexerDelegationsE2E(t *testing.T) {
	srv, n, aliceKP := newIndexerTestServer(t)
	alice, _ := aliceKP.Address()
	nodeAddr, _ := n.keypair.Address()

	// 委託上鏈：以 InsertBlock 構造 pos:delegate 交易（重放累計 delegations 表）。
	blk := &chaindb.Block{Height: 1, Hash: strings.Repeat("b", 64), Proposer: "genesis", Ts: time.Now().Unix()}
	delTx := chaindb.Transaction{
		TxHash: "txdelegidx000000000000000000000000000000000000000000000000000000000000",
		FromAddr: nodeAddr, ToAddr: "pos_stake_pool", Amount: "100", Fee: "0",
		Nonce: 0, Ts: time.Now().Unix(),
		Memo: "pos:delegate:" + alice + ":" + nodeAddr, Status: "confirmed",
	}
	if err := n.db.InsertBlock(blk, []chaindb.Transaction{delTx}); err != nil {
		t.Fatalf("InsertBlock 失敗: %v", err)
	}

	// 全鏈委託分頁。
	resp, err := srv.Client().Get(srv.URL + "/api/v1/delegations")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list["ok"] != true || list["total"].(float64) < 1 {
		t.Fatalf("delegations 應 ok 且 total≥1，got %+v", list)
	}

	// 委託人視角。
	resp2, err := srv.Client().Get(srv.URL + "/api/v1/delegations/" + alice)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var dv map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&dv); err != nil {
		t.Fatal(err)
	}
	if dv["ok"] != true {
		t.Fatalf("delegator 應 ok，got %+v", dv)
	}
	dels, ok := dv["delegations"].([]any)
	if !ok || len(dels) < 1 {
		t.Fatalf("delegator 應有委託，got %+v", dv)
	}
	if dv["deleg_rewards"] == nil {
		t.Fatalf("deleg_rewards 欄位應存在，got %+v", dv)
	}
	// 非法委託人 → 400。
	resp3, err := srv.Client().Get(srv.URL + "/api/v1/delegations/zzz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法委託人應 400，got %d", resp3.StatusCode)
	}
}

// M160：驗證人排名——/api/v1/validators 回權重/委託總額/委託人數。
func TestIndexerValidatorsE2E(t *testing.T) {
	srv, n, aliceKP := newIndexerTestServer(t)
	alice, _ := aliceKP.Address()
	nodeAddr, _ := n.keypair.Address()

	// 驗證人註冊上鏈（pos:stake → pos_validators）＋委託（pos:delegate）。
	if err := n.db.InsertBlock(&chaindb.Block{Height: 1, Hash: strings.Repeat("a", 64), Proposer: "genesis", Ts: time.Now().Unix()},
		[]chaindb.Transaction{{
			TxHash: "v1", FromAddr: nodeAddr, ToAddr: "pos_stake_pool", Amount: "10", Fee: "0",
			Ts: time.Now().Unix(), Memo: "pos:stake:" + nodeAddr, Status: "confirmed",
		}, {
			TxHash: "v2", FromAddr: nodeAddr, ToAddr: "pos_stake_pool", Amount: "100", Fee: "0",
			Ts: time.Now().Unix(), Memo: "pos:delegate:" + alice + ":" + nodeAddr, Status: "confirmed",
		}}); err != nil {
		t.Fatalf("InsertBlock 失敗: %v", err)
	}

	resp, err := srv.Client().Get(srv.URL + "/api/v1/validators")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Ok         bool `json:"ok"`
		Validators []struct {
			Validator    string  `json:"validator"`
			TotalStake   float64 `json:"total_stake"`
			DelegTotal   float64 `json:"deleg_total"`
			DelegatorNum int64   `json:"delegator_count"`
		} `json:"validators"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Ok || len(out.Validators) < 1 {
		t.Fatalf("應至少 1 驗證人，got ok=%v validators=%+v", out.Ok, out.Validators)
	}
	v0 := out.Validators[0]
	if v0.TotalStake != 110 { // 自有 10 ＋ 委託 100
		t.Fatalf("權重應 110，got %v", v0.TotalStake)
	}
	if v0.DelegTotal != 100 || v0.DelegatorNum != 1 {
		t.Fatalf("委託總額應 100／人數應 1，got %v/%v", v0.DelegTotal, v0.DelegatorNum)
	}
}
