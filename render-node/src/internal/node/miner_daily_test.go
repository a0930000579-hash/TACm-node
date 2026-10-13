package node

import (
	"testing"
	"time"

	"tacm/internal/chaindb"
)

// TestMinerEarningsDaily：按日聚合 coinbase:miner 收益（僅統計該地址、近 N 日、UTC+8 日期）。
func TestMinerEarningsDaily(t *testing.T) {
	n, _ := newRewardHost(t, 1)
	miner := "tx0minerA"
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	mkTx := func(hash, to, amt, memo string, ts int64) chaindb.Transaction {
		return chaindb.Transaction{
			TxHash: hash, ToAddr: to, Amount: amt, Fee: "0", Ts: ts, Memo: memo,
			BlockHeight: 1, TxIndex: 0,
		}
	}
	// 三天前 5 TACm、昨天 2.5 TACm、今天 1.25 TACm（僅 minerA 的 coinbase:miner）。
	blocks := []chaindb.Block{
		{Height: 1},
		{Height: 2},
		{Height: 3},
	}
	txs1 := []chaindb.Transaction{
		mkTx("cb-3d", miner, "5", "coinbase:miner", now.AddDate(0, 0, -3).Unix()),
		mkTx("hb-3d", miner, "0", "hb:on:10", now.AddDate(0, 0, -3).Unix()),
	}
	txs2 := []chaindb.Transaction{
		mkTx("cb-1d", miner, "2.5", "coinbase:miner", now.AddDate(0, 0, -1).Unix()),
		mkTx("hb-1d", miner, "0", "hb:on:20", now.AddDate(0, 0, -1).Unix()),
	}
	txs3 := []chaindb.Transaction{
		mkTx("cb-today", miner, "1.25", "coinbase:miner", now.Unix()),
		mkTx("hb-today", miner, "0", "hb:on:30", now.Unix()),
		// 其他地址的交易不應計入。
		mkTx("other-cb", "tx0other", "99", "coinbase:miner", now.Unix()),
	}
	for i, txs := range [][]chaindb.Transaction{txs1, txs2, txs3} {
		if err := n.DB().InsertBlock(&blocks[i], txs); err != nil {
			t.Fatalf("InsertBlock %d: %v", i, err)
		}
	}

	daily, err := n.MinerEarningsDaily(miner, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 7 {
		t.Fatalf("應回 7 日，got %d", len(daily))
	}
	// 第 6 項 = 前天（index 5，因倒數：今天 idx6、昨天 idx5、三天前 idx4）
	idx := map[string]int{}
	for i, d := range daily {
		idx[d.Date] = i
	}
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	today := now.Format("2006-01-02")
	threeDaysAgo := now.AddDate(0, 0, -3).Format("2006-01-02")
	if daily[idx[threeDaysAgo]].Earned != 5 || daily[idx[threeDaysAgo]].Count != 1 {
		t.Errorf("三天前應 5/1，got %+v", daily[idx[threeDaysAgo]])
	}
	if daily[idx[yesterday]].Earned != 2.5 {
		t.Errorf("昨天應 2.5，got %v", daily[idx[yesterday]].Earned)
	}
	if daily[idx[today]].Earned != 1.25 || daily[idx[today]].Count != 1 {
		t.Errorf("今天應 1.25/1，got %+v", daily[idx[today]])
	}

	hd, err := n.MinerHashrateDaily(miner, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(hd) != 7 {
		t.Fatalf("算力應回 7 日，got %d", len(hd))
	}
	if hd[idx[threeDaysAgo]].Hash != 10 {
		t.Errorf("三天前算力應 10，got %v", hd[idx[threeDaysAgo]].Hash)
	}
	if hd[idx[today]].Hash != 30 {
		t.Errorf("今天算力應 30，got %v", hd[idx[today]].Hash)
	}
	if hd[idx[yesterday]].Hash != 20 {
		t.Errorf("昨天算力應 20，got %v", hd[idx[yesterday]].Hash)
	}
}
