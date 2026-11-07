package vrf

// v131：ECVRF 測試——有效證明、篡改拒絕、不同密鑰拒絕、輸出綁定。

import (
	"encoding/hex"
	"testing"
)

// helpers
func origOutputFor(t *testing.T, kp *KeyPair, msg []byte) string {
	t.Helper()
	p, err := kp.Prove(msg)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	return p.Output
}

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }

func privHexOf(kp *KeyPair) string {
	return hex.EncodeToString(kp.priv.Serialize())
}

func TestProveVerify(t *testing.T) {
	kp, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := []byte("lottery-round-42")
	p, err := kp.Prove(msg)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if len(p.Output) != 64 {
		t.Fatalf("輸出應為 32 字節 hex: %s", p.Output)
	}
	ok, err := Verify(kp.Pub, p)
	if err != nil || !ok {
		t.Fatalf("Verify 應成功: %v %v", ok, err)
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	kp, _ := GenerateKey()
	p, err := kp.Prove([]byte("msg-1"))
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	// 篡改 s。
	orig := p.S
	p.S = "0"
	if ok, _ := Verify(kp.Pub, p); ok {
		t.Fatal("篡改 s 後驗證應失敗")
	}
	p.S = orig
	// 篡改輸出。
	p.Output = "ff"
	if ok, _ := Verify(kp.Pub, p); ok {
		t.Fatal("篡改 output 後驗證應失敗")
	}
	// 篡改訊息。
	p.Output = origOutputFor(t, kp, []byte("msg-1"))
	p.Message = hexOf("other-msg")
	if ok, _ := Verify(kp.Pub, p); ok {
		t.Fatal("篡改 message 後驗證應失敗")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	kpA, _ := GenerateKey()
	kpB, _ := GenerateKey()
	p, err := kpA.Prove([]byte("shared-msg"))
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if ok, _ := Verify(kpB.Pub, p); ok {
		t.Fatal("以錯誤公鑰驗證應失敗")
	}
}

func TestDeterministicMessageDiffers(t *testing.T) {
	kp, _ := GenerateKey()
	p1, _ := kp.Prove([]byte("msg-a"))
	p2, _ := kp.Prove([]byte("msg-b"))
	if p1.Output == p2.Output {
		t.Fatal("不同訊息輸出應不同")
	}
	if p1.C == p2.C {
		t.Fatal("不同訊息挑戰應不同")
	}
}

func TestKeyFromPrivateRoundTrip(t *testing.T) {
	kp, _ := GenerateKey()
	kp2, err := KeyFromPrivate(privHexOf(kp))
	if err != nil {
		t.Fatalf("KeyFromPrivate: %v", err)
	}
	if kp2.Pub != kp.Pub {
		t.Fatalf("公鑰不一致: %s vs %s", kp2.Pub, kp.Pub)
	}
	p, err := kp2.Prove([]byte("t"))
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if ok, _ := Verify(kp.Pub, p); !ok {
		t.Fatal("恢復私鑰後證明應可驗證")
	}
}
