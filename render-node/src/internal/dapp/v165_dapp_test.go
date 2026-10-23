package dapp

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// newTestKey 產生測試用 secp256k1 金鑰對，回 (priv, tx0地址, eth地址)。
func newTestKey(t *testing.T, seedByte byte) (priv []byte, tx0Addr, ethAddr string) {
	t.Helper()
	// 決定性私鑰（測試用；非安全來源）。
	priv = make([]byte, 32)
	priv[31] = seedByte
	kp, err := crypto.KeyPairFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	tx0Addr, _ = kp.Address()
	ethAddr, _ = kp.EthAddress()
	return priv, tx0Addr, ethAddr
}

// signFor 用私鑰對指定動作訊息做 EIP-191 簽名，回 hex 字串。
func signFor(t *testing.T, priv []byte, action, address, nonce string, ts int64, payload string) string {
	t.Helper()
	msg := DAppSignMessage(action, address, nonce, ts, payload)
	sig, err := crypto.SignPersonalMessage([]byte(msg), priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return "0x" + hex.EncodeToString(sig)
}

func TestSignatureHappyPathTx0(t *testing.T) {
	s := testStore(t)
	priv, addr, _ := newTestKey(t, 0x11)
	nonce, ts, err := s.IssueNonce(addr, DappActionRegister)
	if err != nil {
		t.Fatal(err)
	}
	payload := "name=MyDApp|url=https://x.com"
	sig := signFor(t, priv, DappActionRegister, addr, nonce, ts, payload)
	if err := s.VerifyWriteSignature(DappActionRegister, addr, nonce, ts, payload, sig); err != nil {
		t.Fatalf("valid sig should pass: %v", err)
	}
}

func TestSignatureHappyPathEthAddress(t *testing.T) {
	s := testStore(t)
	priv, _, ethAddr := newTestKey(t, 0x22)
	nonce, ts, _ := s.IssueNonce(ethAddr, DappActionRate)
	payload := "dapp=1|score=5"
	sig := signFor(t, priv, DappActionRate, ethAddr, nonce, ts, payload)
	if err := s.VerifyWriteSignature(DappActionRate, ethAddr, nonce, ts, payload, sig); err != nil {
		t.Fatalf("valid eth sig should pass: %v", err)
	}
}

func TestSignatureWrongSignerRejected(t *testing.T) {
	s := testStore(t)
	goodPriv, addr, _ := newTestKey(t, 0x33)
	attackerPriv, _, _ := newTestKey(t, 0x44) // 攻擊者私鑰
	nonce, ts, _ := s.IssueNonce(addr, DappActionRegister)
	payload := "name=X|url=https://x.com"
	// 攻擊者用自己的金鑰簽，卻聲稱是 addr。
	sig := signFor(t, attackerPriv, DappActionRegister, addr, nonce, ts, payload)
	if err := s.VerifyWriteSignature(DappActionRegister, addr, nonce, ts, payload, sig); err == nil {
		t.Fatal("forged signature must be rejected")
	}
	_ = goodPriv
}

func TestSignatureReplayNonceRejected(t *testing.T) {
	s := testStore(t)
	priv, addr, _ := newTestKey(t, 0x55)
	nonce, ts, _ := s.IssueNonce(addr, DappActionFavorite)
	payload := "dapp=1"
	sig := signFor(t, priv, DappActionFavorite, addr, nonce, ts, payload)
	if err := s.VerifyWriteSignature(DappActionFavorite, addr, nonce, ts, payload, sig); err != nil {
		t.Fatalf("first use should pass: %v", err)
	}
	// 重放同一 nonce → 應拒絕。
	if err := s.VerifyWriteSignature(DappActionFavorite, addr, nonce, ts, payload, sig); err == nil {
		t.Fatal("replayed nonce must be rejected")
	}
}

func TestSignatureTamperedPayloadRejected(t *testing.T) {
	s := testStore(t)
	priv, addr, _ := newTestKey(t, 0x66)
	// 核發 nonce，用「payload=score=1」簽；驗證時卻送「payload=score=5」→ 訊息不同應拒絕。
	nonce2, ts2, _ := s.IssueNonce(addr, DappActionRate)
	sig2 := signFor(t, priv, DappActionRate, addr, nonce2, ts2, "dapp=1|score=1")
	if err := s.VerifyWriteSignature(DappActionRate, addr, nonce2, ts2, "dapp=1|score=5", sig2); err == nil {
		t.Fatal("signature bound to different payload must be rejected")
	}
}

func TestSignatureBadTimestampRejected(t *testing.T) {
	s := testStore(t)
	priv, addr, _ := newTestKey(t, 0x77)
	nonce, _, _ := s.IssueNonce(addr, DappActionRegister)
	past := time.Now().Unix() - 10000 // 過期
	payload := "name=X|url=https://x.com"
	sig := signFor(t, priv, DappActionRegister, addr, nonce, past, payload)
	if err := s.VerifyWriteSignature(DappActionRegister, addr, nonce, past, payload, sig); err == nil {
		t.Fatal("expired timestamp must be rejected")
	}
}

func TestSignatureMissingSigDegrades(t *testing.T) {
	s := testStore(t)
	_, addr, _ := newTestKey(t, 0x88)
	nonce, ts, _ := s.IssueNonce(addr, DappActionRegister)
	err := s.VerifyWriteSignature(DappActionRegister, addr, nonce, ts, "name=X|url=https://x.com", "")
	if err == nil || !strings.Contains(err.Error(), "缺少簽名") {
		t.Fatalf("missing sig should give explicit degradation, got: %v", err)
	}
}

func TestAdminWhitelist(t *testing.T) {
	t.Setenv("TAC_DAPP_ADMINS", "tx0Admin1, 0xAbc0000000000000000000000000000000000000")
	if !IsDAppAdmin("tx0Admin1") {
		t.Fatal("tx0 admin should be whitelisted")
	}
	if !IsDAppAdmin("0xabc0000000000000000000000000000000000000") {
		t.Fatal("eth admin (case-insensitive) should be whitelisted")
	}
	if IsDAppAdmin("0xNotAdmin0000000000000000000000000000000") {
		t.Fatal("non-whitelisted must be rejected")
	}
}

func TestReviewFlowPendingApprovedFeatured(t *testing.T) {
	s := testStore(t)
	_, addr, _ := newTestKey(t, 0x99)
	d, err := s.Register(addr, "ReviewDApp", "DeFi", "to review", "https://r.com", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "pending" {
		t.Fatalf("new register must be pending: %+v", d)
	}
	// pending 不在公開 featured 列表。
	r, _ := s.List(ListParams{Sort: "featured"})
	for _, it := range r.Items {
		if it.Id == d.Id {
			t.Fatal("pending dapp must not appear in public list")
		}
	}
	// 核准 → 公開可見。
	approved, err := s.SetStatus(d.Id, "approved")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.Featured {
		t.Fatalf("approved state wrong: %+v", approved)
	}
	r, _ = s.List(ListParams{Sort: "featured"})
	found := false
	for _, it := range r.Items {
		if it.Id == d.Id {
			found = true
		}
	}
	if !found {
		t.Fatal("approved dapp must appear in public list")
	}
	// 設為 featured → featured 旗標開。
	feat, err := s.SetStatus(d.Id, "featured")
	if err != nil {
		t.Fatal(err)
	}
	if !feat.Featured || feat.Status != "featured" {
		t.Fatalf("featured state wrong: %+v", feat)
	}
	// 非法狀態被拒。
	if _, err := s.SetStatus(d.Id, "bogus"); err == nil {
		t.Fatal("illegal status must be rejected")
	}
}
