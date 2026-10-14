package node

// community_rewards_test.go — M73-B：追蹤 RPC、互動積分、鏈上獎勵結算 e2e。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// testAddr 產生有效錢包地址（base58check）。
func testAddr(t *testing.T) string {
	t.Helper()
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	a, err := kp.Address()
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	return a
}

// newCommunityHost 建立帶創世（社群基金 100000 TACm）的節點＋RPC host。
func newCommunityHost(t *testing.T, blockTime int) (*Node, *httptest.Server) {
	t.Helper()
	cfg := testCfg(t)
	cfg.BlockTime = blockTime
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pool, err := n.CommunityPoolAddress()
	if err != nil {
		t.Fatalf("CommunityPoolAddress: %v", err)
	}
	now := time.Now().Unix()
	genTx := chaindb.Transaction{
		TxHash: "gen-community-pool", ToAddr: pool,
		Amount: "100000", Fee: "0", Ts: now, Memo: "genesis:community_pool",
	}
	g := &chaindb.Block{Height: 0, Hash: strings.Repeat("0", 64), Proposer: "genesis", Ts: now, TxCount: 1}
	if err := n.DB().InsertBlock(g, []chaindb.Transaction{genTx}); err != nil {
		t.Fatalf("創世失敗: %v", err)
	}
	srv := NewRPCServer(n)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = n.Close() })
	return n, ts
}

func cmPostJSON(t *testing.T, url string, body map[string]any) map[string]any {
	t.Helper()
	var b strings.Builder
	_ = json.NewEncoder(&b).Encode(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("POST %s 解析: %v", url, err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("POST %s 狀態 %d: %v", url, resp.StatusCode, out)
	}
	return out
}

func cmDelJSON(t *testing.T, url string, body map[string]any) {
	t.Helper()
	var b strings.Builder
	_ = json.NewEncoder(&b).Encode(body)
	req, _ := http.NewRequest("DELETE", url, strings.NewReader(b.String()))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE %s 狀態 %d", url, resp.StatusCode)
	}
}

func cmGetJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("GET %s 解析: %v", url, err)
	}
	return out
}

// TestCommunityFollowRPC 追蹤 API＋追蹤中動態牆。
func TestCommunityFollowRPC(t *testing.T) {
	_, ts := newCommunityHost(t, 0)
	alice := testAddr(t)
	bob := testAddr(t)
	carol := testAddr(t)

	cmPostJSON(t, ts.URL+"/api/community/post", map[string]any{"address": alice, "kind": "post", "content": "hi alice"})
	cmPostJSON(t, ts.URL+"/api/community/post", map[string]any{"address": bob, "kind": "post", "content": "hi bob"})
	cmPostJSON(t, ts.URL+"/api/community/post", map[string]any{"address": carol, "kind": "post", "content": "hi carol"})

	cmPostJSON(t, ts.URL+"/api/community/follow", map[string]any{"follower": alice, "followee": bob})
	// 追蹤中動態牆：alice 自己 + bob（2 筆），不含 carol。
	feed := cmGetJSON(t, ts.URL+"/api/community/feed?scope=following&address="+alice)
	if len(feed["items"].([]any)) != 2 {
		t.Fatalf("追蹤動態牆應 2 筆")
	}
	// 全部動態：3 筆。
	all := cmGetJSON(t, ts.URL+"/api/community/feed")
	if len(all["items"].([]any)) != 3 {
		t.Fatalf("全部動態應 3 筆")
	}
	// following 列表。
	fw := cmGetJSON(t, ts.URL+"/api/community/following?address="+alice)
	if len(fw["items"].([]any)) != 1 {
		t.Fatalf("following 應 1 筆")
	}
	// 取消追蹤。
	cmDelJSON(t, ts.URL+"/api/community/follow", map[string]any{"follower": alice, "followee": bob})
	fw2 := cmGetJSON(t, ts.URL+"/api/community/following?address="+alice)
	if len(fw2["items"].([]any)) != 0 {
		t.Fatalf("取消追蹤後應為空")
	}
}

// TestCommunityRewardsRPC 互動積分＋rewards 查詢。
func TestCommunityRewardsRPC(t *testing.T) {
	n, ts := newCommunityHost(t, 0)
	alice := testAddr(t)
	// 發文 +5、按讚 +1、留言 +3 = 9 分。
	pid := cmPostJSON(t, ts.URL+"/api/community/post", map[string]any{"address": alice, "kind": "post", "content": "hello"})
	id := int64(pid["post_id"].(float64))
	cmPostJSON(t, ts.URL+"/api/community/post/"+fmt.Sprintf("%d", id)+"/like", map[string]any{"address": alice})
	cmPostJSON(t, ts.URL+"/api/community/post/"+fmt.Sprintf("%d", id)+"/comments", map[string]any{"address": alice, "body": "nice"})

	rw := cmGetJSON(t, ts.URL+"/api/community/rewards?address="+alice)
	if rw["my_points"].(float64) != 9 {
		t.Fatalf("積分應 9，得 %v", rw["my_points"])
	}
	if rw["interval"].(float64) != float64(CommunityRewardInterval) {
		t.Fatalf("interval 異常: %v", rw["interval"])
	}
	top := rw["top"].([]any)
	if len(top) != 1 {
		t.Fatalf("排行應 1 人，得 %d", len(top))
	}
	pts, _ := n.communitySvc.Points(alice)
	if pts != 9 {
		t.Fatalf("Store 積分應 9，得 %d", pts)
	}
}

// TestSettleCommunityRewards 結算上鏈：達週期後 mempool 出現帶簽名獎勵交易。
func TestSettleCommunityRewards(t *testing.T) {
	n, ts := newCommunityHost(t, 0)
	alice := testAddr(t)
	bob := testAddr(t)
	if err := n.communitySvc.AddPoints(alice, 10); err != nil {
		t.Fatal(err)
	}
	if err := n.communitySvc.AddPoints(bob, 5); err != nil {
		t.Fatal(err)
	}
	pool, _ := n.CommunityPoolAddress()
	if got := parseTACM(n.db.GetBalance(pool)); got != 100000 {
		t.Fatalf("社群基金餘額應 100000，得 %v", got)
	}
	// 未達週期：tip=0、meta.last=0 → tip < 0+60 → 不結算。
	if err := n.communitySvc.SetRewardMeta(0, 0); err != nil {
		t.Fatal(err)
	}
	nSettled, err := n.SettleCommunityRewards(59)
	if err != nil {
		t.Fatalf("Settle(59): %v", err)
	}
	if nSettled != 0 {
		t.Fatalf("未達週期不應結算，得 %d", nSettled)
	}
	// 達週期：結算 2 人，mempool 出現 2 筆 community:reward。
	nSettled, err = n.SettleCommunityRewards(60)
	if err != nil {
		t.Fatalf("Settle(60): %v", err)
	}
	if nSettled != 2 {
		t.Fatalf("應結算 2 人，得 %d", nSettled)
	}
	mempool, err := n.db.GetMempool(100)
	if err != nil {
		t.Fatal(err)
	}
	var rewardTxs int
	total := 0.0
	for _, m := range mempool {
		memo := m["memo"].(string)
		if strings.HasPrefix(memo, "community:reward:") {
			rewardTxs++
			total += parseTACM(m["amount"].(string))
		}
	}
	if rewardTxs != 2 {
		t.Fatalf("mempool 獎勵交易應 2 筆，得 %d", rewardTxs)
	}
	if total > CommunityRewardPerPeriod+1e-6 || total < CommunityRewardPerPeriod-1e-6 {
		t.Fatalf("期總額應 %v，得 %v", CommunityRewardPerPeriod, total)
	}
	// 期號推進。
	meta, _ := n.communitySvc.GetRewardMeta()
	if meta.Period != 1 || meta.LastTip != 60 {
		t.Fatalf("meta 應 P1/60，得 %+v", meta)
	}
	// 手動 settle API 再次觸發（未達新週期 → 0 人）。
	r := cmPostJSON(t, ts.URL+"/api/community/settle", nil)
	if r["ok"] != true || r["settled"].(float64) != 0 {
		t.Fatalf("settle API 應 ok/0: %v", r)
	}
}

// TestCommunityRewardOnChain 結算交易出塊上鏈：獲獎者鏈上餘額增加。
func TestCommunityRewardOnChain(t *testing.T) {
	n, _ := newCommunityHost(t, 1)
	alice := testAddr(t)
	bob := testAddr(t)
	if err := n.communitySvc.AddPoints(alice, 10); err != nil {
		t.Fatal(err)
	}
	if err := n.communitySvc.AddPoints(bob, 5); err != nil {
		t.Fatal(err)
	}
	if err := n.communitySvc.SetRewardMeta(0, 0); err != nil {
		t.Fatal(err)
	}
	beforeA := n.db.GetBalance(alice)
	n.Start()
	defer func() { _ = n.Close() }()
	// 觸發一期結算（期號 1，週期 60 塊），獎勵交易進 mempool。
	if _, err := n.SettleCommunityRewards(60); err != nil {
		t.Fatalf("結算: %v", err)
	}
	// 等獎勵交易打包上鏈。
	waitFor(t, func() bool {
		return parseTACM(n.db.GetBalance(alice)) > parseTACM(beforeA)
	}, 10*time.Second, "社群獎勵未上鏈")
	// 鏈上審計守恆（M71 ledger 重放）不受影響。
	la, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatalf("LedgerAudit: %v", err)
	}
	if !la.LedgerOK {
		t.Fatalf("ledger 守恆被破壞: %+v", la)
	}
}
