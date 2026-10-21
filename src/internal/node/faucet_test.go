package node

import (
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestSendFromNode 節點金鑰發送轉帳：交易入池→入塊→接收方餘額增加。
func TestSendFromNode(t *testing.T) {
	n, _, _ := contractStudioSetup(t)
	kp, _ := crypto.GenerateKeyPair()
	to, _ := kp.Address()
	// 節點靠 coinbase 累積餘額：等 from 有幣再發（block-time 1s，數秒內到位）。
	fromKP := n.keypair
	from, _ := fromKP.Address()
	waitFor(t, func() bool {
		bal := n.DB().GetBalance(from)
		return bal != "0" && bal != ""
	}, 10*time.Second, "節點 coinbase 餘額未就緒")
	// 測試節點 coinbase 累積較慢，用可負擔金額驗證發送機制；產品端額度由 faucet 配額控制。
	txHash, from, err := n.SendFromNode(to, "0.1")
	if err != nil {
		t.Fatalf("SendFromNode: %v", err)
	}
	if txHash == "" || from == "" {
		t.Fatalf("SendFromNode 回傳空: hash=%q from=%q", txHash, from)
	}
	// 等入塊：接收方餘額（鏈上 TxView/address 層）增加。
	waitFor(t, func() bool {
		rows, rerr := n.DB().GetTransactionsByAddress(to, 50)
		return rerr == nil && len(rows) > 0
	}, 8*time.Second, "faucet 轉帳未入塊")
}
