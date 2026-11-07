package aa

// M154 AA 單元測試：UserOperation 驗證（簽名/pubkey/hex/欄位）、估算、操作紀錄。

import (
	"encoding/hex"
	"strings"
	"testing"

	"tacm/internal/crypto"
)

// signedOp 構造已由 kp 簽名的 UserOperation（callData 預設 "00"）。
func signedOp(t *testing.T, kp *crypto.KeyPair) *UserOperation {
	t.Helper()
	addr, err := kp.Address()
	if err != nil {
		t.Fatal(err)
	}
	op := &UserOperation{
		Sender: addr, Nonce: 0, Target: addr, // target 用有效地址（格式驗證）
		CallData: "00", CallGasLimit: 100000, PreVerificationGas: 21000,
		MaxFeePerGas: "0.000001", MaxPriorityFee: "0",
		Pubkey: hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	digest, err := UserOpDigest(op)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.SignDigest(digest, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	op.Signature = hex.EncodeToString(sig)
	return op
}

func newTestAA() *AA {
	return New("tx0bundler00000000000000000000000000000")
}

func TestAAValidateValidOp(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	a := newTestAA()
	if err := a.Validate(signedOp(t, kp)); err != nil {
		t.Fatalf("合法操作應通過: %v", err)
	}
}

func TestAAValidateBadSignature(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op := signedOp(t, kp)
	op.Signature = strings.Repeat("00", 70) // 偽造簽名
	if err := newTestAA().Validate(op); err == nil || !strings.Contains(err.Error(), "invalid signature") {
		t.Fatalf("偽造簽名應被拒，got %v", err)
	}
}

func TestAAValidatePubkeyMismatch(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	other, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op := signedOp(t, kp)
	otherPub := hex.EncodeToString(other.PublicKeyCompressed())
	op.Pubkey = otherPub // pubkey 與 sender 不符
	if err := newTestAA().Validate(op); err == nil || !strings.Contains(err.Error(), "不符") {
		t.Fatalf("pubkey 不符應被拒，got %v", err)
	}
}

func TestAAValidateBadCallData(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op := signedOp(t, kp)
	op.CallData = "zzzz" // 非 hex
	if err := newTestAA().Validate(op); err == nil || !strings.Contains(err.Error(), "call_data") {
		t.Fatalf("非 hex call_data 應被拒，got %v", err)
	}
	op2 := signedOp(t, kp)
	op2.CallData = "" // 空
	if err := newTestAA().Validate(op2); err == nil || !strings.Contains(err.Error(), "call_data") {
		t.Fatalf("空 call_data 應被拒，got %v", err)
	}
}

func TestAAValidateBadGas(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op := signedOp(t, kp)
	op.MaxFeePerGas = "0" // fee 為 0
	if err := newTestAA().Validate(op); err == nil || !strings.Contains(err.Error(), "max_fee") {
		t.Fatalf("fee=0 應被拒，got %v", err)
	}
}

func TestAAEstimate(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op := signedOp(t, kp)
	gas, fee, err := newTestAA().Estimate(op)
	if err != nil {
		t.Fatal(err)
	}
	// callData "00" = 1 byte → 16 gas；總計 21000+100000+16。
	if gas != 121016 {
		t.Fatalf("gas 應 121016，got %d", gas)
	}
	if fee != 0.121016 {
		t.Fatalf("fee 應 0.121016，got %g", fee)
	}
	if _, _, err := newTestAA().Estimate(&UserOperation{}); err == nil {
		t.Fatal("空操作估算應失敗")
	}
}

func TestAARecordAndOperations(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op := signedOp(t, kp)
	a := newTestAA()
	a.Record(op, "hash1", "0.12", "")
	a.Record(op, "hash2", "0.13", "boom") // 第二筆失敗
	ops := a.Operations(op.Sender)
	if len(ops) != 2 {
		t.Fatalf("應有 2 筆操作，got %d", len(ops))
	}
	if ops[0].TxHash != "hash2" || ops[0].Status != "rejected" || ops[0].Error != "boom" {
		t.Fatalf("最新紀錄應為失敗的 hash2，got %+v", ops[0])
	}
	if ops[1].TxHash != "hash1" || ops[1].Status != "submitted" {
		t.Fatalf("第二筆應為 hash1 submitted，got %+v", ops[1])
	}
	if len(a.Operations("tx0nonexistent000000000000000000000000")) != 0 {
		t.Fatal("無紀錄 sender 應回空")
	}
}
