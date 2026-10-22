package rollup

// v156：有效性證明鏈路整合測試——誠實批次通過；篡改 post root / 交易 /
// post 餘額一律被 internal/zk 無狀態驗證拒絕；批次序號正確。

import (
	"testing"

	"tacm/internal/crypto"
	"tacm/internal/zk"
)

func TestValidityProofHonestBatch(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()

	r.DepositToL2(alice, 1000, "g1")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 100, 1, 0))
	block, err := r.CreateBatch()
	if err != nil || block == nil {
		t.Fatalf("打包失敗: %v", err)
	}
	if block.BatchIndex != 1 {
		t.Errorf("首批序號應為 1, got %d", block.BatchIndex)
	}

	p, err := r.ValidityProofFor(block.Height)
	if err != nil {
		t.Fatalf("產生有效性證明失敗: %v", err)
	}
	if err := zk.VerifyRollupValidityProof(p); err != nil {
		t.Fatalf("誠實批次驗證應通過: %v", err)
	}
	if err := r.VerifyBatchValidity(block.Height); err != nil {
		t.Fatalf("VerifyBatchValidity 應通過: %v", err)
	}
	if p.TxRoot == "" || p.PostRoot == "" || len(p.Touched) != 2 {
		t.Errorf("證明不完整: %+v", p)
	}
}

func TestValidityProofRejectsTamperedPostRoot(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g1")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 100, 1, 0))
	block, _ := r.CreateBatch()

	p, _ := r.ValidityProofFor(block.Height)
	p.PostRoot = "0x000000000000000000000000000000000000000000000000000000000000dead"
	if err := zk.VerifyRollupValidityProof(p); err == nil {
		t.Fatal("篡改 post root 後驗證必須失敗")
	}
}

func TestValidityProofRejectsTamperedTxAmount(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g1")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 100, 1, 0))
	block, _ := r.CreateBatch()

	p, _ := r.ValidityProofFor(block.Height)
	p.Txs[0].Amount = "999" // 偷改金額
	if err := zk.VerifyRollupValidityProof(p); err == nil {
		t.Fatal("篡改交易金額後驗證必須失敗（約束重放不符）")
	}
}

func TestValidityProofRejectsTamperedPostBalance(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g1")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 100, 1, 0))
	block, _ := r.CreateBatch()

	p, _ := r.ValidityProofFor(block.Height)
	// 找到 bob 葉偷改餘額。
	for i := range p.Touched {
		if p.Touched[i].Address == bob {
			p.Touched[i].PostBalance = "999"
		}
	}
	if err := zk.VerifyRollupValidityProof(p); err == nil {
		t.Fatal("偷改 post 餘額（偷鑄）後驗證必須失敗")
	}
	_ = alice
}

func TestValidityBatchIndexSequence(t *testing.T) {
	r := newTestRollup(t)
	defer r.Close()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	r.DepositToL2(alice, 1000, "g1")
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 10, 1, 0))
	b1, _ := r.CreateBatch()
	r.SubmitTransaction(l2SignedTx(t, aliceKP, bob, 20, 1, 1))
	b2, _ := r.CreateBatch()
	if b1.BatchIndex != 1 || b2.BatchIndex != 2 {
		t.Errorf("批次序號錯誤: b1=%d b2=%d", b1.BatchIndex, b2.BatchIndex)
	}
	list := r.BatchesList(10)
	if len(list) != 2 {
		t.Fatalf("批次列表應 2 筆, got %d", len(list))
	}
	if _, err := r.ValidityProofFor(b2.Height); err != nil {
		t.Fatalf("第二批證明產生失敗: %v", err)
	}
	sr, err := r.CurrentStateRoot()
	if err != nil || sr.StateRoot == "" || !sr.Valid {
		t.Errorf("狀態根視圖錯誤: %+v err=%v", sr, err)
	}
}
