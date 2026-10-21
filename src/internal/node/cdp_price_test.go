package node

import (
	"math/big"
	"testing"

	"tacm/internal/dex"
)

// TestDexPriceForTACM DEX 池聚合 TACm 參考價（M149s：CDP 價格鏈上化）。
func TestDexPriceForTACM(t *testing.T) {
	mk := func(t0, t1 string, r0, r1 int64) *dex.Pool {
		return &dex.Pool{Token0: t0, Token1: t1, Reserve0: big.NewInt(r0), Reserve1: big.NewInt(r1)}
	}
	// tacm/tiusd：1 TACm = 2 TiUSD → 價 2.0。
	p1 := mk("tacm", "tiusd", 100, 200)
	// tacm/usdt：1 TACm = 1.5 USDT → 價 1.5（應被第一個命中攔截）。
	p2 := mk("tacm", "usdt", 200, 300)
	// 無 tacm 池 → 0。
	p3 := mk("tiusd", "usdt", 100, 100)
	// tacm/eth 池（非穩定幣配對）→ 忽略。
	p4 := mk("tacm", "eth", 100, 100)

	if v := dexPriceForTACM([]*dex.Pool{p1}); v != 2.0 {
		t.Fatalf("tacm/tiusd 價應 2.0, got %v", v)
	}
	if v := dexPriceForTACM([]*dex.Pool{p1, p2}); v != 2.0 {
		t.Fatalf("多池應取第一個命中 2.0, got %v", v)
	}
	if v := dexPriceForTACM([]*dex.Pool{p3}); v != 0 {
		t.Fatalf("無 tacm 池應 0, got %v", v)
	}
	if v := dexPriceForTACM([]*dex.Pool{p4}); v != 0 {
		t.Fatalf("tacm/eth 非穩定幣配對應忽略, got %v", v)
	}
	if v := dexPriceForTACM(nil); v != 0 {
		t.Fatalf("空池應 0, got %v", v)
	}
}
