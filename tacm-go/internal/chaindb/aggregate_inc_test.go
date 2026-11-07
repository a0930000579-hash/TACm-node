package chaindb

import (
	"math"
	"testing"
)

// cbTx 建構一筆指定 memo/金額、歸屬某高度的 coinbase 交易。
func cbTx(h int64, idx int, memo, amount string) Transaction {
	return Transaction{
		TxHash: "inc-" + memo + "-" + string(rune('a'+idx)) + "-" + string(rune('a'+h)),
		ToAddr: "to-" + memo, Amount: amount, Fee: "0", Nonce: 0,
		Ts: h, Memo: memo, Status: "confirmed",
	}
}

func incBlock(h int64, prev string, txs []Transaction) *Block {
	return &Block{
		Height: h, Hash: "bh-" + string(rune('a'+h)), PrevHash: &prev,
		Proposer: "p", Ts: h, TxCount: len(txs), Difficulty: 1,
	}
}

var incMemos = []struct{ memo, amt string }{
	{"coinbase", "10"},
	{"coinbase:pool", "1.5"},
	{"coinbase:node", "4.5"},
	{"coinbase:miner", "4"},
}

// insertPhase 插入 (lo, hi] 高度的區塊，每塊含四類 coinbase 與一筆不計入的普通交易。
func insertPhase(t *testing.T, c *ChainDB, lo, hi int64, prev string) string {
	t.Helper()
	for h := lo + 1; h <= hi; h++ {
		txs := make([]Transaction, 0, len(incMemos)+1)
		for i, m := range incMemos {
			txs = append(txs, cbTx(h, i, m.memo, m.amt))
		}
		txs = append(txs, Transaction{
			TxHash: "plain-" + string(rune('a'+h)), FromAddr: "a", ToAddr: "b",
			Amount: "99", Fee: "0", Nonce: 0, Ts: h, Memo: "transfer", Status: "confirmed",
		})
		b := incBlock(h, prev, txs)
		if err := c.InsertBlock(b, txs); err != nil {
			t.Fatalf("insert h=%d: %v", h, err)
		}
		prev = b.Hash
	}
	return prev
}

func sameTotals(t *testing.T, name string, a, b CoinbaseTotals) {
	t.Helper()
	if a.Count != b.Count || math.Abs(a.Total-b.Total) > 1e-9 ||
		math.Abs(a.Pool-b.Pool) > 1e-9 || math.Abs(a.Node-b.Node) > 1e-9 {
		t.Fatalf("%s 不一致: %+v vs %+v", name, a, b)
	}
}

// TestCoinbaseTotalsSinceMatchesFull：分階段插入，每階段 GetCoinbaseTotalsSince(上一 tip)
// 必須等於該階段全量總額的增量，且累加後與最終全量 GetCoinbaseTotals 完全一致（鎖口徑）。
func TestCoinbaseTotalsSinceMatchesFull(t *testing.T) {
	c := openTmpDB(t)
	zero := "0000000000000000000000000000000000000000000000000000000000000000"
	gen := incBlock(0, zero, nil)
	if err := c.InsertBlock(gen, nil); err != nil {
		t.Fatalf("insert genesis: %v", err)
	}

	phases := []struct{ lo, hi int64 }{{0, 2}, {2, 4}, {4, 6}}
	prev := gen.Hash
	var acc CoinbaseTotals
	var prevFull CoinbaseTotals
	for _, ph := range phases {
		prev = insertPhase(t, c, ph.lo, ph.hi, prev)
		full, err := c.GetCoinbaseTotals()
		if err != nil {
			t.Fatal(err)
		}
		// 增量查詢：僅回上一 tip 之後的新塊。
		inc, err := c.GetCoinbaseTotalsSince(ph.lo)
		if err != nil {
			t.Fatal(err)
		}
		want := CoinbaseTotals{
			Count: full.Count - prevFull.Count,
			Total: full.Total - prevFull.Total,
			Pool:  full.Pool - prevFull.Pool,
			Node:  full.Node - prevFull.Node,
		}
		sameTotals(t, "phase increment", inc, want)
		acc.Count += inc.Count
		acc.Total += inc.Total
		acc.Pool += inc.Pool
		acc.Node += inc.Node
		prevFull = full
	}

	final, err := c.GetCoinbaseTotals()
	if err != nil {
		t.Fatal(err)
	}
	sameTotals(t, "accumulated vs final", acc, final)

	// 期望值：每塊 10+1.5+4.5+4=20，共 6 塊 → total 120；pool 9、node 27、count 24。
	if math.Abs(acc.Total-120) > 1e-9 || math.Abs(acc.Pool-9) > 1e-9 ||
		math.Abs(acc.Node-27) > 1e-9 || acc.Count != 24 {
		t.Fatalf("期望值錯誤: %+v", acc)
	}
}
