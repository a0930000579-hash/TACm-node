package chaindb

import "testing"

// TestForEachCoinbaseAmount：串流回調的金額集合必須與 GetCoinbaseTransactions 完全一致，
// 且只含 coinbase 前綴交易（排除普通交易）。
func TestForEachCoinbaseAmount(t *testing.T) {
	c := openTmpDB(t)
	zero := "0000000000000000000000000000000000000000000000000000000000000000"
	gen := incBlock(0, zero, nil)
	if err := c.InsertBlock(gen, nil); err != nil {
		t.Fatal(err)
	}
	insertPhase(t, c, 0, 3, gen.Hash)

	want, err := c.GetCoinbaseTransactions()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	err = c.ForEachCoinbaseAmount(func(amount string) error {
		got = append(got, amount)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("筆數 got=%d want=%d", len(got), len(want))
	}
	for i, tx := range want {
		if got[i] != tx.Amount {
			t.Fatalf("第 %d 筆金額 got=%s want=%s", i, got[i], tx.Amount)
		}
	}
}
