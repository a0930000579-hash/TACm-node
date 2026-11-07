package zk

// v156：Rollup 有效性證明測試——有效批次通過；任何篡改（交易、post root、
// post 餘額、負餘額、nonce 重用、供給不守恆）都必須被驗證拒絕。

import (
	"encoding/hex"
	"strconv"
	"testing"

	"tacm/internal/crypto"
)

// validBatch 建構一筆誠實批次的證明（alice 1000 → bob 100，fee 1）。
func validBatch(t *testing.T) (*RollupValidityProof, *crypto.KeyPair, *crypto.KeyPair) {
	t.Helper()
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()

	txMap := map[string]any{
		"from": alice, "to": bob,
		"amount": "100", "fee": "1",
		"nonce": 0, "ts": 1000,
		"pubkey": hex.EncodeToString(aliceKP.PublicKeyCompressed()),
	}
	sig, err := crypto.SignTransaction(txMap, aliceKP.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	txs := []RollupTx{{
		TxHash: "0xtxhash1",
		From:   alice, To: bob,
		Amount: "100", Fee: "1", Nonce: 0, Ts: 1000,
		Signature: sig, Pubkey: txMap["pubkey"].(string),
	}}
	touched := []RollupAccTransition{
		{Address: alice, PreBalance: "1000", PreNonce: 0, PostBalance: "899", PostNonce: 1},
		{Address: bob, PreBalance: "0", PreNonce: 0, PostBalance: "100", PostNonce: 0},
	}
	p, err := BuildRollupValidityProof(1, "0xprevroot", 1000, txs, touched,
		aliceKP.PrivateKey(), hex.EncodeToString(aliceKP.PublicKeyCompressed()))
	if err != nil {
		t.Fatal(err)
	}
	return p, aliceKP, bobKP
}

func TestValidityProofValid(t *testing.T) {
	p, _, _ := validBatch(t)
	if err := VerifyRollupValidityProof(p); err != nil {
		t.Fatalf("誠實批次應通過: %v", err)
	}
	if p.TxRoot == "" || p.PostRoot == "" || p.Signature == "" {
		t.Fatalf("證明欄位不完整: %+v", p)
	}
}

func TestValidityRejectsTamperedTxAmount(t *testing.T) {
	p, _, _ := validBatch(t)
	// 篡改交易金額（承諾未變但約束輸入變了）→ tx_root/約束重放失敗。
	p.Txs[0].Amount = "200"
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("篡改交易金額後驗證應失敗")
	}
}

func TestValidityRejectsTamperedTxHash(t *testing.T) {
	p, _, _ := validBatch(t)
	p.Txs[0].TxHash = "0xtampered"
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("篡改 tx hash 後 tx_root 應不符")
	}
}

func TestValidityRejectsTamperedPostRoot(t *testing.T) {
	p, _, _ := validBatch(t)
	p.PostRoot = "0xdeadbeef"
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("篡改 post root 後驗證應失敗")
	}
}

func TestValidityRejectsTamperedPostBalance(t *testing.T) {
	p, _, _ := validBatch(t)
	// 把 bob post 餘額改成 999（偷鑄）→ 逐葉比對＋守恆應雙重失敗。
	p.Touched[1].PostBalance = "999"
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("篡改 post 餘額（偷鑄）後驗證應失敗")
	}
}

func TestValidityRejectsNegativeBalance(t *testing.T) {
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	// 發 999 但只夠 100 → 負餘額違規。
	txMap := map[string]any{
		"from": alice, "to": bob, "amount": "999", "fee": "1",
		"nonce": 0, "ts": 1000,
		"pubkey": hex.EncodeToString(aliceKP.PublicKeyCompressed()),
	}
	sig, _ := crypto.SignTransaction(txMap, aliceKP.PrivateKey())
	txs := []RollupTx{{
		TxHash: "0xt1", From: alice, To: bob, Amount: "999", Fee: "1",
		Nonce: 0, Ts: 1000, Signature: sig, Pubkey: txMap["pubkey"].(string),
	}}
	touched := []RollupAccTransition{
		{Address: alice, PreBalance: "100", PreNonce: 0, PostBalance: "-900", PostNonce: 1},
		{Address: bob, PreBalance: "0", PreNonce: 0, PostBalance: "999", PostNonce: 0},
	}
	p, err := BuildRollupValidityProof(1, "0xprev", 1000, txs, touched, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("負餘額（-900）應被拒")
	}
}

func TestValidityRejectsDoubleSpendNonce(t *testing.T) {
	p, aliceKP, bobKP := validBatch(t)
	// 第二筆交易重用同一 nonce 0 → 雙花/重放。
	alice, _ := aliceKP.Address()
	carolKP, _ := crypto.GenerateKeyPair()
	carol, _ := carolKP.Address()
	txMap := map[string]any{
		"from": alice, "to": carol, "amount": "10", "fee": "1",
		"nonce": 0, "ts": 1001,
		"pubkey": hex.EncodeToString(aliceKP.PublicKeyCompressed()),
	}
	sig, _ := crypto.SignTransaction(txMap, aliceKP.PrivateKey())
	p.Txs = append(p.Txs, RollupTx{
		TxHash: "0xtxhash2", From: alice, To: carol, Amount: "10", Fee: "1",
		Nonce: 0, Ts: 1001, Signature: sig, Pubkey: txMap["pubkey"].(string),
	})
	// 重建證明（讓承諾與新交易集一致），只留 nonce 重用作為約束違規。
	p2, err := BuildRollupValidityProof(1, "0xprevroot", 1000, p.Txs,
		append(p.Touched, RollupAccTransition{
			Address: carol, PreBalance: "0", PreNonce: 0, PostBalance: "10", PostNonce: 0,
		}), nil, "")
	_ = bobKP
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRollupValidityProof(p2); err == nil {
		t.Fatal("nonce 重用（雙花）應被拒")
	}
}

func TestValidityRejectsConservationViolation(t *testing.T) {
	p, _, _ := validBatch(t)
	// 把 alice post 餘額改成 900（少扣 1，供給不守恆）。
	p.Touched[0].PostBalance = "900"
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("供給不守恆應被拒")
	}
}

func TestValidityRejectsForgedSignature(t *testing.T) {
	p, aliceKP, _ := validBatch(t)
	// 保持原提議者公鑰，但用別的私鑰簽 → 偽造。
	other, _ := crypto.GenerateKeyPair()
	sig, err := crypto.SignDigest(p.digestCore(), other.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	p.Signature = hex.EncodeToString(sig)
	_ = aliceKP
	if err := VerifyRollupValidityProof(p); err == nil {
		t.Fatal("偽造批簽名應被拒")
	}
}

func TestCanonicalAmountRoundTrip(t *testing.T) {
	// 確認 'f',-1 格式與簽名語境一致（保證真實交易可被重放驗證）。
	s := strconv.FormatFloat(100.0, 'f', -1, 64)
	if s != "100" {
		t.Fatalf("canonical amount 格式錯誤: %q", s)
	}
}
