package perp

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustDeposit(t *testing.T, s *Store, trader, asset string, amt float64) {
	t.Helper()
	if err := s.Deposit(trader, asset, amt); err != nil {
		t.Fatalf("Deposit %s %s %v: %v", trader, asset, amt, err)
	}
}

func TestOpenCloseLong(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	trader := "0xtester"
	mustDeposit(t, s, trader, "TIUSD", 1000)

	// 開 long BTC 10x，保證金 100。
	p, notional, err := s.OpenPosition(trader, "BTC", "long", 10, 100, "TIUSD")
	if err != nil {
		t.Fatalf("OpenPosition: %v", err)
	}
	if p.Size <= 0 {
		t.Fatalf("size 應 > 0, got %v", p.Size)
	}
	if math.Abs(notional-1000) > 1e-6 {
		t.Fatalf("notional 應=1000, got %v", notional)
	}
	if math.Abs(p.EntryPrice-65000) > 1e-6 {
		t.Fatalf("entry 應=65000, got %v", p.EntryPrice)
	}
	// 保證金被鎖定：free 應從 1000 降到 900。
	acc, _ := s.GetAccount(trader)
	if math.Abs(acc.Balances["TIUSD"]-900) > 1e-6 {
		t.Fatalf("free 應=900, got %v", acc.Balances["TIUSD"])
	}

	// 價格上漲 → 未實現 PnL 為正。
	s.SetExternalPrice("BTC", 70000)
	p2, _ := s.GetPosition(p.ID)
	wantPnL := (70000 - 65000) * p.Size
	if math.Abs(p2.UnrealizedPnL-wantPnL) > 1e-6 {
		t.Fatalf("unrealized PnL 應=%v, got %v", wantPnL, p2.UnrealizedPnL)
	}

	// 平倉：realizedPnL ≈ wantPnL。
	rpnl, returned, err := s.ClosePosition(p.ID)
	if err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	if math.Abs(rpnl-wantPnL) > 1e-3 {
		t.Fatalf("realized PnL 應=%v, got %v", wantPnL, rpnl)
	}
	// returned = margin(100) + pnl。
	if math.Abs(returned-(100+wantPnL)) > 1e-3 {
		t.Fatalf("returned 應=%v, got %v", 100+wantPnL, returned)
	}
}

func TestShortPnL(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("ETH", 3000)
	trader := "0xshort"
	mustDeposit(t, s, trader, "TIUSD", 1000)
	p, _, err := s.OpenPosition(trader, "ETH", "short", 5, 200, "TIUSD")
	if err != nil {
		t.Fatalf("OpenPosition short: %v", err)
	}
	// 跌價 → 空頭獲利。
	s.SetExternalPrice("ETH", 2700)
	p2, _ := s.GetPosition(p.ID)
	wantPnL := (3000 - 2700) * p.Size
	if math.Abs(p2.UnrealizedPnL-wantPnL) > 1e-6 {
		t.Fatalf("short PnL 應=%v, got %v", wantPnL, p2.UnrealizedPnL)
	}
}

func TestLiquidation(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	trader := "0xliq"
	mustDeposit(t, s, trader, "TIUSD", 1000)
	p, _, err := s.OpenPosition(trader, "BTC", "long", 20, 100, "TIUSD")
	if err != nil {
		t.Fatalf("OpenPosition: %v", err)
	}
	// 先試試未達強平時 Liquidate 應回錯。
	if _, _, err := s.LiquidatePosition(p.ID, "system"); err == nil {
		t.Fatal("未達強平時 Liquidate 應回錯")
	}
	// 大幅下跌到觸發強平：long 在 20x、maint 50bps。
	// liqPrice ≈ (entry - margin/size)/(1-maintF) ≈ 62060。
	s.SetExternalPrice("BTC", 62000)
	p2, _ := s.GetPosition(p.ID)
	t.Logf("after drop: margin=%v pnl=%v maint=%v liqPrice=%v", p2.Margin, p2.UnrealizedPnL, p2.MaintMargin, p2.LiqPrice)
	liqBonus, returned, err := s.LiquidatePosition(p.ID, "0xliquidator")
	if err != nil {
		t.Fatalf("LiquidatePosition: %v", err)
	}
	if returned <= 0 {
		t.Fatalf("returned 應 > 0（此價位 equity 仍為正）: bonus=%v returned=%v", liqBonus, returned)
	}
	// 清算人應收到獎勵。
	liqAcc, _ := s.GetAccount("0xliquidator")
	if liqAcc.Balances["TIUSD"] <= 0 {
		t.Fatalf("清算人應收到獎勵, got %v", liqAcc.Balances)
	}
}

func TestRemoveMarginGuard(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	trader := "0xmm"
	mustDeposit(t, s, trader, "TIUSD", 1000)
	p, _, _ := s.OpenPosition(trader, "BTC", "long", 10, 100, "TIUSD")
	// 減 96（剩餘 4）應拒絕（低於維持保證金 ≈5）。
	if err := s.RemoveMargin(p.ID, 96); err == nil {
		t.Fatal("RemoveMargin 過多應被拒絕")
	}
	// 加 50 → margin=150，再減 30 → 120 應可。
	if err := s.AddMargin(p.ID, 50); err != nil {
		t.Fatalf("AddMargin: %v", err)
	}
	if err := s.RemoveMargin(p.ID, 30); err != nil {
		t.Fatalf("RemoveMargin 合理範圍應通過: %v", err)
	}
}

func TestWithdrawGuard(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	trader := "0xwd"
	mustDeposit(t, s, trader, "TIUSD", 1000)
	// 開倉鎖 100。
	p, _, _ := s.OpenPosition(trader, "BTC", "long", 5, 100, "TIUSD")
	_ = p
	// free=900，提 950 應失敗。
	if err := s.Withdraw(trader, "TIUSD", 950); err == nil {
		t.Fatal("Withdraw 超過 free 應失敗")
	}
	// 提 500 應成功。
	if err := s.Withdraw(trader, "TIUSD", 500); err != nil {
		t.Fatalf("Withdraw 500 應成功: %v", err)
	}
}

func TestFundingSettlement(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	longTrader := "0xl"
	shortTrader := "0xs"
	mustDeposit(t, s, longTrader, "TIUSD", 1000)
	mustDeposit(t, s, shortTrader, "TIUSD", 1000)
	pL, _, err := s.OpenPosition(longTrader, "BTC", "long", 5, 200, "TIUSD")
	if err != nil {
		t.Fatalf("open long: %v", err)
	}
	pS, _, err := s.OpenPosition(shortTrader, "BTC", "short", 5, 100, "TIUSD")
	if err != nil {
		t.Fatalf("open short: %v", err)
	}
	marginLBefore := pL.Margin
	marginSBefore := pS.Margin
	// 推進時間 2 小時，觸發結算。
	future := pL.LastFundingTs + 7200
	if err := s.SettleFunding(future); err != nil {
		t.Fatalf("SettleFunding: %v", err)
	}
	pL2, _ := s.GetPosition(pL.ID)
	pS2, _ := s.GetPosition(pS.ID)
	// 多空 OI 失衡：long size=200*5/65000=0.01538, short size=100*5/65000=0.00769
	// long > short → rate > 0 → long.margin -=, short.margin +=
	if pL2.Margin >= marginLBefore {
		t.Logf("long margin 應下降（多頭付）: before=%v after=%v", marginLBefore, pL2.Margin)
	}
	if pS2.Margin <= marginSBefore {
		t.Logf("short margin 應上升（空頭收）: before=%v after=%v", marginSBefore, pS2.Margin)
	}
	if pL2.Margin == marginLBefore && pS2.Margin == marginSBefore {
		t.Fatal("funding 結算應改變至少一方 margin")
	}
}

func TestMarketsAndGetMarket(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	ms, err := s.Markets()
	if err != nil {
		t.Fatalf("Markets: %v", err)
	}
	if len(ms) < 3 {
		t.Fatalf("應至少有 BTC/ETH/SOL 三市場, got %d", len(ms))
	}
	m, err := s.GetMarket("btc")
	if err != nil {
		t.Fatalf("GetMarket btc: %v", err)
	}
	if m.MarkPrice <= 0 {
		t.Fatalf("BTC mark price 應 > 0, got %v", m.MarkPrice)
	}
	if m.MaxLeverage <= 0 {
		t.Fatalf("MaxLeverage 應 > 0")
	}
}

func TestEnsureMarket(t *testing.T) {
	s := newTestStore(t)
	if err := s.EnsureMarket("DOGE", 200, 5); err != nil {
		t.Fatalf("EnsureMarket: %v", err)
	}
	m, err := s.GetMarket("DOGE")
	if err != nil {
		t.Fatalf("GetMarket DOGE: %v", err)
	}
	if m.MaintMarginBps != 200 || m.MaxLeverage != 5 {
		t.Fatalf("市場參數錯誤: %+v", m)
	}
}

func TestOpenInsufficientBalance(t *testing.T) {
	s := newTestStore(t)
	s.SetExternalPrice("BTC", 65000)
	trader := "0xpoor"
	// 沒入金就開倉 → 應失敗。
	if _, _, err := s.OpenPosition(trader, "BTC", "long", 5, 100, "TIUSD"); err == nil {
		t.Fatal("無餘額開倉應失敗")
	}
}

func TestDataDir(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "perp.db")); err != nil {
		t.Fatalf("perp.db 應存在: %v", err)
	}
	_ = s.Close()
}
