package node

// perp_e2e_test.go — 永續合約 e2e：
//  A) deposit TiUSD → open long BTC 10x margin=100 → 持倉存在、size>0、方向正確。
//  B) 推送下跌價格使未實現 PnL 變負 → 再推到觸發強平線 → Liquidate 成功。
//  C) 未達強平時 Liquidate 應回錯（對照）。
//  D) 另開一倉平倉，realizedPnL 合理。
//
// 依賴：node 已掛 n.perpSt（*perp.Store）、n.walletSvc、n.oracleSvc。

import (
	"testing"

	"tacm/internal/wallet"
)

// perpTestAddr 派生一個 0x 地址（與 aimTestAddr 同邏輯）。
func perpTestAddr(t *testing.T) string {
	t.Helper()
	_, tx0 := aimTestAddr(t)
	return tx0
}

// TestPerpOpenCloseE2E 基本開平倉流程。
func TestPerpOpenCloseE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	if n.perpSt == nil {
		t.Skip("perp 未啟用（整合者尚未掛 n.perpSt）")
	}
	trader := perpTestAddr(t)
	// 造錢包資金（TACm）。
	if err := n.walletSvc.Reward(trader, wallet.AssetTACm,
		wallet.NewAmountF(50000, wallet.AssetTACm), "perp:test"); err != nil {
		t.Fatalf("造 TACm 資金: %v", err)
	}
	// 推送 BTC 起始價格。
	n.perpSt.SetExternalPrice("BTC", 65000)

	// 入金 1000 TACm 到 perp。
	dep := postJSON(t, srv.URL+"/api/perp/deposit",
		map[string]any{"address": trader, "asset": "TACm", "amount": 1000})
	if dep["ok"] != true {
		t.Fatalf("deposit 失敗: %v", dep)
	}

	// 開 long BTC 10x margin=100。
	op := postJSON(t, srv.URL+"/api/perp/position/open",
		map[string]any{"address": trader, "symbol": "BTC", "side": "long",
			"leverage": 10, "margin": 100, "margin_asset": "TACm"})
	if op["ok"] != true {
		t.Fatalf("open 失敗: %v", op)
	}
	pos, _ := op["position"].(map[string]any)
	if pos == nil {
		t.Fatal("回傳 position 為空")
	}
	if pos["side"] != "long" {
		t.Fatalf("side 應為 long, got %v", pos["side"])
	}
	size, _ := pos["size"].(float64)
	if size <= 0 {
		t.Fatalf("size 應 > 0, got %v", size)
	}
	pid, _ := pos["id"].(float64)

	// 下跌 → 未實現 PnL 應變負。
	n.perpSt.SetExternalPrice("BTC", 60000)
	ps := getJSON(t, srv.URL+"/api/perp/positions?trader="+trader)
	psArr, _ := ps["positions"].([]any)
	if len(psArr) != 1 {
		t.Fatalf("應有 1 個 open 倉位, got %d", len(psArr))
	}
	p0, _ := psArr[0].(map[string]any)
	pnl, _ := p0["unrealized_pnl"].(float64)
	if pnl >= 0 {
		t.Fatalf("下跌後 long 未實現 PnL 應為負, got %v", pnl)
	}

	// 平倉。
	cl := postJSON(t, srv.URL+"/api/perp/position/close",
		map[string]any{"position_id": int64(pid)})
	if cl["ok"] != true {
		t.Fatalf("close 失敗: %v", cl)
	}
	rpnl, _ := cl["realized_pnl"].(float64)
	if rpnl >= 0 {
		t.Fatalf("下跌平倉 long realizedPnL 應為負, got %v", rpnl)
	}
}

// TestPerpLiquidateE2E 觸發強平流程。
func TestPerpLiquidateE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	if n.perpSt == nil {
		t.Skip("perp 未啟用")
	}
	trader := perpTestAddr(t)
	liquidator := perpTestAddr(t)
	if err := n.walletSvc.Reward(trader, wallet.AssetTiUSD,
		wallet.NewAmountF(5000, wallet.AssetTiUSD), "perp:test"); err != nil {
		t.Fatalf("造 TiUSD 資金: %v", err)
	}
	n.perpSt.SetExternalPrice("BTC", 65000)

	// 入金 1000 TiUSD。
	dep := postJSON(t, srv.URL+"/api/perp/deposit",
		map[string]any{"address": trader, "asset": "TIUSD", "amount": 1000})
	if dep["ok"] != true {
		t.Fatalf("deposit 失敗: %v", dep)
	}

	// 開 long 20x margin=100（高槓桿容易強平）。
	op := postJSON(t, srv.URL+"/api/perp/position/open",
		map[string]any{"address": trader, "symbol": "BTC", "side": "long",
			"leverage": 20, "margin": 100, "margin_asset": "TIUSD"})
	if op["ok"] != true {
		t.Fatalf("open 失敗: %v", op)
	}
	pos, _ := op["position"].(map[string]any)
	pid, _ := pos["id"].(float64)

	// 對照：未達強平時 Liquidate 應回錯。
	neg := postJSON(t, srv.URL+"/api/perp/liquidate",
		map[string]any{"position_id": int64(pid), "liquidator": liquidator})
	if neg["ok"] == true {
		t.Fatal("未達強平時 Liquidate 應失敗")
	}

	// 推到強平線附近（liqPrice ≈ 62060）。
	n.perpSt.SetExternalPrice("BTC", 62000)
	liq := postJSON(t, srv.URL+"/api/perp/liquidate",
		map[string]any{"position_id": int64(pid), "liquidator": liquidator})
	if liq["ok"] != true {
		t.Fatalf("達強平線 Liquidate 應成功: %v", liq)
	}
}

// TestPerpAccountAndMarketsE2E 帳戶與市場查詢。
func TestPerpAccountAndMarketsE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	if n.perpSt == nil {
		t.Skip("perp 未啟用")
	}
	ms := getJSON(t, srv.URL+"/api/perp/markets")
	if ms["ok"] != true {
		t.Fatalf("markets 失敗: %v", ms)
	}
	arr, _ := ms["markets"].([]any)
	if len(arr) < 3 {
		t.Fatalf("應至少 3 個市場, got %d", len(arr))
	}
	trader := perpTestAddr(t)
	acc := getJSON(t, srv.URL+"/api/perp/account?trader="+trader)
	if acc["ok"] != true {
		t.Fatalf("account 失敗: %v", acc)
	}
}
