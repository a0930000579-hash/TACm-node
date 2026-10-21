package p2p

import (
	"bytes"
	"testing"
)

// TestSecureECDH 兩節點 ECDH 共享密鑰一致（對稱性）。
func TestSecureECDH(t *testing.T) {
	a, err := NewSecure()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSecure()
	if err != nil {
		t.Fatal(err)
	}
	keyAB, err := a.SharedKey(b.PubKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	keyBA, err := b.SharedKey(a.PubKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyAB[:], keyBA[:]) {
		t.Fatal("ECDH 共享密鑰不對稱")
	}
}

// TestSecureEncryptRoundtrip AES-GCM 加解密往返＋竄改偵測。
func TestSecureEncryptRoundtrip(t *testing.T) {
	a, _ := NewSecure()
	b, _ := NewSecure()
	key, _ := a.SharedKey(b.PubKeyHex())

	plain := []byte(`{"kind":"block","from":"node1"}`)
	sealed, err := EncryptPayload(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sealed, plain) {
		t.Fatal("密文不應等於明文")
	}
	// 兩次加密 nonce 不同（密文不同）
	sealed2, _ := EncryptPayload(key, plain)
	if bytes.Equal(sealed, sealed2) {
		t.Fatal("兩次加密密文應不同（隨機 nonce）")
	}
	got, err := DecryptPayload(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("解密結果與明文不符")
	}
	// 錯誤金鑰解密應失敗
	wrong, _ := a.SharedKey(a.PubKeyHex())
	if _, err := DecryptPayload(wrong, sealed); err == nil {
		t.Fatal("錯誤金鑰解密應失敗")
	}
	// 竄改密文應失敗
	sealed[12] ^= 0xff
	if _, err := DecryptPayload(key, sealed); err == nil {
		t.Fatal("竄改密文應被偵測")
	}
}
