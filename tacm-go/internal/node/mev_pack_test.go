package node

import (
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// TestMEVPackTipOrderInBlock 驗證 produceBlock 已改用有效小費打包排序：
// 兩個已出資地址各一筆 nonce=0 交易，高費者應在區塊內先於低費者；
// 同時斷言 base_fee 已在取 mempool 前算出（區塊仍帶 base_fee）。
func TestMEVPackTipOrderInBlock(t *testing.T) {
	cfg := testCfg(t)
	cfg.BlockTime = 5 // 拉長出塊間隔，留出視窗讓兩筆交易都進 mempool 再出塊
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatal(err)
	}

	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, aliceKP)
	insertGenesisWithAlloc(t, n, alice) // alice 與節點(admin)各獲 1000
	bobKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bob := mustAddr(t, bobKP)

	n.Start()
	defer n.Close()

	// alice 低費、admin（節點 keypair）高費，均 nonce=0。
	lowTx := signedTx(t, aliceKP, bob, "1", "0.03", 0)
	lowHash, err := n.SubmitTransaction(lowTx)
	if err != nil {
		t.Fatalf("alice 低費交易提交失敗: %v", err)
	}
	highTx := signedTx(t, n.keypair, alice, "1", "0.10", 0)
	highHash, err := n.SubmitTransaction(highTx)
	if err != nil {
		t.Fatalf("admin 高費交易提交失敗: %v", err)
	}

	// 等待出至少一個區塊。
	waitFor(t, func() bool { return n.DB().GetTipHeight() >= 1 }, 10*time.Second, "MEV 打包區塊未出塊")

	b, err := n.DB().GetBlock(1)
	if err != nil || b == nil {
		t.Fatalf("讀取區塊 1 失敗: %v", err)
	}
	if b.BaseFee == "" || b.BaseFee == "0" {
		t.Fatalf("區塊應帶 base_fee（baseFee 應已在取 mempool 前算出），got %q", b.BaseFee)
	}

	txs, err := n.DB().GetTransactionsByBlock(1)
	if err != nil {
		t.Fatal(err)
	}
	idxOf := make(map[string]int)
	for i, tx := range txs {
		idxOf[tx.TxHash] = i
	}
	lowIdx, okLow := idxOf[lowHash]
	highIdx, okHigh := idxOf[highHash]
	if !okLow || !okHigh {
		t.Fatalf("兩筆交易應在同一區塊：low=%v(%d) high=%v(%d) 區塊交易數=%d",
			okLow, lowIdx, okHigh, highIdx, len(txs))
	}
	// 高費（高有效小費）應排在低費之前。
	if highIdx > lowIdx {
		t.Fatalf("MEV 打包順序錯誤：高費 idx=%d 應在低費 idx=%d 之前", highIdx, lowIdx)
	}
	t.Logf("MEV 打包：高費 idx=%d、低費 idx=%d，base_fee=%s", highIdx, lowIdx, b.BaseFee)

	// 順便驗證預設策略字串解析。
	if chaindb.ParsePackStrategy("priority_tip") != chaindb.PackPriorityTip {
		t.Fatal("預設策略應為 priority_tip")
	}
}
