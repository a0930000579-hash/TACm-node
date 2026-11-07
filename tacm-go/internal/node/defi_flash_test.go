package node

// defi_flash_test.go — 閃電貸 e2e：
//  1) 成功：借 1000 → 同請求償還本金＋費 0.09%
//  2) 償還資金不足 → 錯誤＋整體回滾（借貸池餘額無淨變動）

import (
	"testing"

	"tacm/internal/defi"
	"tacm/internal/wallet"
)

func TestDefiFlashLoan(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	user := "tx0flashuser"
	asset := "TACm"

	// 借貸池資金與借款人償還資金。
	if err := n.walletSvc.Reward(defi.LendingAccount, wallet.Asset(asset),
		wallet.NewAmountF(50000, wallet.Asset(asset)), "test:pool"); err != nil {
		t.Fatalf("池資金: %v", err)
	}
	if err := n.walletSvc.Reward(user, wallet.Asset(asset),
		wallet.NewAmountF(2000, wallet.Asset(asset)), "test:user"); err != nil {
		t.Fatalf("借款人資金: %v", err)
	}

	// 1) 成功：借 1000，費 = 0.9。
	r := postJSON(t, srv.URL+"/api/defi/flash_loan",
		map[string]any{"address": user, "asset": asset, "amount": 1000.0})
	if r["ok"] != true {
		t.Fatalf("閃電貸失敗: %v", r)
	}
	fee, ok := r["fee"].(float64)
	if !ok || fee != 1000*0.0009 {
		t.Fatalf("費率錯誤: %v", r["fee"])
	}

	// 2) 無自有資金（償還需 本金＋費 > 僅借款額）→ 錯誤＋整體回滾（池餘額不變）。
	poor := "tx0flashpoor"
	before, _ := n.walletSvc.Balance(defi.LendingAccount)
	r2 := postJSON(t, srv.URL+"/api/defi/flash_loan",
		map[string]any{"address": poor, "asset": asset, "amount": 1000.0})
	if r2["ok"] == true {
		t.Fatalf("無償還資金應失敗: %v", r2)
	}
	after, _ := n.walletSvc.Balance(defi.LendingAccount)
	if before.TACmBalance.Cmp(after.TACmBalance) != 0 {
		t.Fatalf("回滾失敗：池餘額變動 before=%v after=%v", before.TACmBalance, after.TACmBalance)
	}
}
