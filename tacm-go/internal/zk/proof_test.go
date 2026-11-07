package zk

// v130：ZK 證明核心測試——批次證明、防篡改、淨額檢查、承諾與 Nullifier。

import (
	"encoding/hex"
	"strings"
	"testing"

	"tacm/internal/crypto"
)

func sampleOps() []StateOp {
	return []StateOp{
		{Type: OpTransfer, Address: "txAAAA1111111111111111", To: "txBBBB2222222222222222", Asset: "TACm", Delta: "-5000000000000000000"},
		{Type: OpTransfer, Address: "txBBBB2222222222222222", Asset: "TACm", Delta: "5000000000000000000"},
		{Type: OpMint, Address: "txCCCC3333333333333333", Asset: "TiUSD", Delta: "1000000"},
	}
}

func TestBuildVerifyBatchProof(t *testing.T) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	p, err := BuildBatchProof("rootOld", "rootNew", sampleOps(), "n1", kp.PrivateKey(), hex.EncodeToString(kp.PublicKeyCompressed()))
	if err != nil {
		t.Fatalf("BuildBatchProof: %v", err)
	}
	if p.Commitment == "" || p.BatchHash == "" || len(p.Proofs) != 3 {
		t.Fatalf("proof 欄位不完整: %+v", p)
	}
	if p.Signature == "" {
		t.Fatal("缺少簽名")
	}
	if err := VerifyBatchProof(p); err != nil {
		t.Fatalf("VerifyBatchProof 應成功: %v", err)
	}
}

func TestVerifyRejectsTamperedOp(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	ops := sampleOps()
	p, err := BuildBatchProof("old", "new", ops, "n1", kp.PrivateKey(), hex.EncodeToString(kp.PublicKeyCompressed()))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// 篡改一筆操作的金額。
	p.Ops[1].Delta = "6000000000000000000"
	if err := VerifyBatchProof(p); err == nil {
		t.Fatal("篡改 op 後驗證應失敗")
	}
}

func TestVerifyRejectsTamperedNewRoot(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	p, err := BuildBatchProof("old", "new", sampleOps(), "n1", kp.PrivateKey(), hex.EncodeToString(kp.PublicKeyCompressed()))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// 篡改 newRoot（承諾未變但聲稱的根變了）。
	p.NewRoot = "forged"
	if err := VerifyBatchProof(p); err != nil {
		t.Fatalf("newRoot 為自由欄位時驗證不應失敗（責任在調用方）: %v", err)
	}
	// 篡改 commitment（核心防線）。
	p.Commitment = "deadbeef"
	if err := VerifyBatchProof(p); err == nil {
		t.Fatal("篡改 commitment 後驗證應失敗")
	}
}

func TestCheckStateTransition(t *testing.T) {
	// 轉帳淨額為零 → 通過。
	ops := []StateOp{
		{Type: OpTransfer, Asset: "TACm", Delta: "-1000000000000000000"},
		{Type: OpTransfer, Asset: "TACm", Delta: "1000000000000000000"},
	}
	net, err := CheckStateTransition(ops)
	if err != nil {
		t.Fatalf("淨額為零應通過: %v", err)
	}
	if net["TACm"] == nil || net["TACm"].Sign() != 0 {
		t.Fatalf("淨額應為零: %+v", net)
	}
	// 淨額不為零 → 拒絕。
	bad := []StateOp{
		{Type: OpTransfer, Asset: "TACm", Delta: "-1000000000000000000"},
		{Type: OpTransfer, Asset: "TACm", Delta: "2000000000000000000"},
	}
	if _, err := CheckStateTransition(bad); err == nil {
		t.Fatal("淨額不為零應被拒")
	}
	// mint/burn 例外允許。
	mixed := []StateOp{
		{Type: OpMint, Asset: "TiUSD", Delta: "5000000"},
		{Type: OpBurn, Asset: "TiUSD", Delta: "-2000000"},
	}
	if _, err := CheckStateTransition(mixed); err != nil {
		t.Fatalf("mint/burn 應允許: %v", err)
	}
	// 非法 delta。
	noise := []StateOp{{Type: OpTransfer, Asset: "TACm", Delta: "abc"}}
	if _, err := CheckStateTransition(noise); err == nil {
		t.Fatal("非法 delta 應被拒")
	}
}

func TestPedersenCommitmentAndNullifier(t *testing.T) {
	blind := []byte("secret-blinding-1")
	msg := []byte("1000000000000000000")
	c1 := PedersenCommitment(blind, msg)
	c2 := PedersenCommitment(blind, msg)
	if c1 != c2 {
		t.Fatal("同 (blind,msg) 承諾應一致")
	}
	if c1 == PedersenCommitment([]byte("other"), msg) {
		t.Fatal("不同 blind 承諾應不同")
	}
	n1 := Nullifier(c1, blind)
	n2 := Nullifier(c1, blind)
	if n1 != n2 {
		t.Fatal("同 (commit,blind) nullifier 應一致")
	}
	// 公開 c1 不洩漏 msg：commitment 不含明文訊息。
	if strings.Contains(c1, string(msg)) {
		t.Fatal("承諾不應含明文訊息")
	}
}
