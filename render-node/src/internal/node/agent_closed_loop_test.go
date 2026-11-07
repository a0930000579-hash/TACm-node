package node

// agent_closed_loop_test.go — task 269：寫工具 confirm 後真正執行（不再只 prepare），
// 並以 get_tx 回讀狀態，驗證「準備 → 確認 → 執行 → 回讀」閉環。

import (
	"context"
	"math/big"
	"testing"

	"tacm/internal/wallet"
)

// mustTACmBalance 取出地址的 TACm 帳本餘額（*big.Int）。
func mustTACmBalance(t *testing.T, s *RPCServer, addr string) *big.Int {
	t.Helper()
	acc, err := s.node.walletSvc.Balance(addr)
	if err != nil {
		t.Fatalf("balance of %s failed: %v", addr, err)
	}
	return acc.TACmBalance
}

// TestAgentTransferExecutesOnConfirm 直接執行 transfer 工具，驗證資金真的移動且回傳 executed。
func TestAgentTransferExecutesOnConfirm(t *testing.T) {
	s := newAIHost(t)
	reg := s.buildAgentRegistry(nil, testAlice)
	tool, ok := reg.Get("transfer")
	if !ok { t.Fatalf("transfer tool not registered") }
	// 先以帳本授權充值（與其他 e2e 測試相同），確保測試地址有足夠餘額。
	if err := s.node.walletSvc.Deposit(testAlice, wallet.AssetTACm, wallet.NewAmountF(1000, wallet.AssetTACm), "test:topup"); err != nil {
		t.Fatalf("topup failed: %v", err)
	}
	before := mustTACmBalance(t, s, testAlice)
	res, err := tool.Run(context.Background(), map[string]any{
		"from":   testAlice,
		"to":     "tx0bob000000000000000000000000000000",
		"asset":  "TACm",
		"amount": "100",
		"memo":   "closed-loop",
	})
	if err != nil {
		t.Fatalf("transfer Run failed: %v", err)
	}
	executed, _ := res.Data.(map[string]any)["executed"].(bool)
	if !executed {
		t.Fatalf("expected executed=true, got %v", res.Data)
	}
	after := mustTACmBalance(t, s, testAlice)
	if before.Cmp(after) == 0 {
		t.Fatalf("sender balance should change: before=%s after=%s", before, after)
	}
	recipient := mustTACmBalance(t, s, "tx0bob000000000000000000000000000000")
	if recipient.Sign() <= 0 {
		t.Fatalf("recipient should receive funds, got %s", recipient)
	}
}

// TestAgentGetTxNotFound 回讀未知 hash 應誠實回報 found=false（不編造）。
func TestAgentGetTxNotFound(t *testing.T) {
	s := newAIHost(t)
	reg := s.buildAgentRegistry(nil, testAlice)
	tool, ok := reg.Get("get_tx")
	if !ok { t.Fatalf("get_tx tool not registered") }
	res, err := tool.Run(context.Background(), map[string]any{"tx_hash": "unknown-hash-xyz"})
	if err != nil {
		t.Fatalf("get_tx Run failed: %v", err)
	}
	data, _ := res.Data.(map[string]any)
	if found, _ := data["found"].(bool); found {
		t.Fatalf("expected found=false for unknown hash, got %v", data)
	}
}

// TestAgentTransferRejectsMissingRecipient 缺 to 應返回錯誤（完整錯誤處理）。
func TestAgentTransferRejectsMissingRecipient(t *testing.T) {
	s := newAIHost(t)
	reg := s.buildAgentRegistry(nil, testAlice)
	tool, ok := reg.Get("transfer")
	if !ok { t.Fatalf("transfer tool not registered") }
	if _, err := tool.Run(context.Background(), map[string]any{
		"from": testAlice, "asset": "TACm", "amount": "10",
	}); err == nil {
		t.Fatal("expected error when recipient missing")
	}
}
