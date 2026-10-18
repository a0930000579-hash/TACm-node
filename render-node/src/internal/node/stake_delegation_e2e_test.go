package node

// M156 質押委託 e2e：委託交易上鏈 → 委託紀錄與驗證人權重更新 → 撤銷。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

func newStakeTestServer(t *testing.T) (*httptest.Server, *Node, *crypto.KeyPair) {
	t.Helper()
	n, err := New(testCfg(t), "stk1", 3)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	nodeAddr, _ := n.keypair.Address()
	insertGenesisWithAlloc(t, n, alice)
	insertGenesisWithAlloc(t, n, nodeAddr) // 節點鏈上餘額（委託交易代簽付款）
	n.Start()
	// 站內帳本充值（委託鎖倉用；創世分配同步發生在 New 時，故另存）。
	amt, _ := wallet.NewAmount("10000", wallet.AssetTACm)
	if err := n.walletSvc.Deposit(alice, wallet.AssetTACm, amt, "test seed"); err != nil {
		t.Fatalf("wallet 充值失敗: %v", err)
	}
	return httptest.NewServer(NewRPCServer(n).Handler()), n, aliceKP
}

func postStake(t *testing.T, srv *httptest.Server, path string, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := srv.Client().Post(srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s 失敗: %v", path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("POST %s 解析失敗: %v", path, err)
	}
	return out
}

func getStake(t *testing.T, srv *httptest.Server, path string) map[string]any {
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

func TestStakeDelegationE2E(t *testing.T) {
	srv, n, aliceKP := newStakeTestServer(t)
	alice, _ := aliceKP.Address()
	nodeAddr, _ := n.keypair.Address()

	// 委託 100 TACm 給節點驗證人。
	out := postStake(t, srv, "/stake/delegate", map[string]any{
		"from": alice, "validator": nodeAddr, "amount": "100",
	})
	if out["ok"] != true {
		t.Fatalf("委託應 ok，got %+v", out)
	}
	// 等委託交易打包（節點出塊 ≥1）。
	waitFor(t, func() bool {
		b, err := n.db.GetLatestBlock()
		return err == nil && b != nil && b.Height >= 1
	}, 15*time.Second, "委託交易未打包")

	// 狀態查詢：我的委託。
	st := getStake(t, srv, "/stake/status?address="+alice)
	if st["ok"] != true {
		t.Fatalf("status 應 ok，got %+v", st)
	}
	dels, _ := st["my_delegations"].([]any)
	found := false
	for _, d := range dels {
		dm := d.(map[string]any)
		if dm["validator"] == nodeAddr {
			found = true
			if dm["amount"].(float64) != 100 {
				t.Fatalf("委託額應 100，got %v", dm["amount"])
			}
		}
	}
	if !found {
		t.Fatalf("委託紀錄未找到 validator=%s，got %+v", nodeAddr, st)
	}

	// 驗證人權重包含委託。
	vs, err := n.db.GetPosValidators()
	if err != nil {
		t.Fatal(err)
	}
	weightOK := false
	for _, v := range vs {
		if v.Address == nodeAddr && v.TotalStake >= 100 {
			weightOK = true
		}
	}
	if !weightOK {
		t.Fatalf("驗證人權重應 ≥100，got %+v", vs)
	}

	// 撤銷 40。
	u := postStake(t, srv, "/stake/undelegate", map[string]any{
		"from": alice, "validator": nodeAddr, "amount": "40",
	})
	if u["ok"] != true {
		t.Fatalf("撤銷應 ok，got %+v", u)
	}
	waitFor(t, func() bool {
		b, _ := n.db.GetLatestBlock()
		return b != nil && b.Height >= 2
	}, 15*time.Second, "撤銷交易未打包")
	st2 := getStake(t, srv, "/stake/status?address="+alice)
	dels2, _ := st2["my_delegations"].([]any)
	for _, d := range dels2 {
		dm := d.(map[string]any)
		if dm["validator"] == nodeAddr {
			if dm["amount"].(float64) != 60 {
				t.Fatalf("撤銷後委託應 60，got %v", dm["amount"])
			}
		}
	}

	// 非法金額 → 400。
	bad := postStake(t, srv, "/stake/delegate", map[string]any{
		"from": alice, "validator": nodeAddr, "amount": "-5",
	})
	if bad["ok"] == true {
		t.Fatal("負金額委託應被拒")
	}
	_ = http.StatusBadRequest
}

func TestStakeDelegationRewardQueryE2E(t *testing.T) {
	srv, n, aliceKP := newStakeTestServer(t)
	alice, _ := aliceKP.Address()
	// 用 InsertBlock 構造含 coinbase:deleg 交易的區塊（公開 API，重放一致）。
	blk := &chaindb.Block{Height: 1, Hash: strings.Repeat("a", 64), Proposer: "genesis", Ts: time.Now().Unix()}
	delTx := chaindb.Transaction{
		TxHash: "txdeleg0000000000000000000000000000000000000000000000000000000000000001",
		FromAddr: "", ToAddr: alice, Amount: "1.25", Fee: "0",
		Nonce: 0, Ts: time.Now().Unix(), Memo: chaindb.DelegRewardMemo, Status: "confirmed",
	}
	if err := n.db.InsertBlock(blk, []chaindb.Transaction{delTx}); err != nil {
		t.Fatalf("InsertBlock 失敗: %v", err)
	}
	st := getStake(t, srv, "/stake/status?address="+alice)
	if st["ok"] != true {
		t.Fatalf("status 應 ok，got %+v", st)
	}
	if rw, ok := st["deleg_rewards"].(float64); !ok || rw <= 0 {
		t.Fatalf("deleg_rewards 應 >0，got %v (%T)", st["deleg_rewards"], st["deleg_rewards"])
	}
}

// M159：委託解綁定時限——撤銷後資產鎖倉（unbondings pending）、錢包不即時返還；
// 期滿由出塊節點自動返還（pos:unbond_pay）。
func TestStakeUnbondingE2E(t *testing.T) {
	srv, n, aliceKP := newStakeTestServer(t)
	alice, _ := aliceKP.Address()
	nodeAddr, _ := n.keypair.Address()

	// 委託 100。
	out := postStake(t, srv, "/stake/delegate", map[string]any{
		"from": alice, "validator": nodeAddr, "amount": "100",
	})
	if out["ok"] != true {
		t.Fatalf("委託應 ok，got %+v", out)
	}
	// 撤銷 100。
	out2 := postStake(t, srv, "/stake/undelegate", map[string]any{
		"from": alice, "validator": nodeAddr, "amount": "100",
	})
	if out2["ok"] != true {
		t.Fatalf("撤銷應 ok，got %+v", out2)
	}
	if out2["unbonding_period"] == nil {
		t.Fatalf("撤銷應回 unbonding_period，got %+v", out2)
	}
	// 撤銷交易打包後：status 回 unbondings pending。
	waitFor(t, func() bool {
		st := getStake(t, srv, "/stake/status?address="+alice)
		ubs, _ := st["unbondings"].([]any)
		return len(ubs) >= 1
	}, 15*time.Second, "解綁佇列未出現")
	st := getStake(t, srv, "/stake/status?address="+alice)
	ubs, _ := st["unbondings"].([]any)
	u0 := ubs[0].(map[string]any)
	if u0["status"] != "pending" {
		t.Fatalf("解綁應 pending，got %+v", u0)
	}
	completeH, ok := u0["complete_height"].(float64)
	if !ok || completeH <= 0 {
		t.Fatalf("complete_height 應 >0，got %+v", u0)
	}
	// 錢包不即時返還：委託人鏈上委託餘額仍為 0（鎖倉未歸還）。
	dl, err := n.db.GetDelegations(alice)
	if err != nil || len(dl) != 0 || dl != nil && len(dl) > 0 && dl[0].Amount != 0 {
		t.Fatalf("撤銷後 active 委託應為空，got %+v err %v", dl, err)
	}
}
