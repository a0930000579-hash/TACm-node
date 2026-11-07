package p2p

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// v65：sealBody 對端不支援加密 → 自動降級明文並標記通道狀態。
func TestSealBodyDowngrade(t *testing.T) {
	// 對端無 /p2p/pubkey（模擬舊版明文節點）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p2p/pubkey" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New(Config{Encrypt: true, DialTimeout: 2e9, RequestTimeout: 5e9}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 僅測試 sealBody 路徑；模擬 Start 設置的 ctx（真實運行由 Start(ctx) 提供）。
	n.ctx = context.Background()

	plain := []byte(`{"node_id":"n1","height":1}`)
	out, err := n.sealBody(srv.URL+"/p2p/hello", plain)
	if err != nil {
		t.Fatalf("sealBody 不應報錯（降級明文）: %v", err)
	}
	if string(out) != string(plain) {
		t.Fatalf("對端無加密能力時應原樣返回明文，got %q", out)
	}
	if n.peerEncrypted(srv.URL) {
		t.Fatal("降級後 peerEncrypted 應為 false")
	}
}

// v65：sealBody 對端支援加密 → 回加密 envelope 並標記加密。
func TestSealBodyEncrypted(t *testing.T) {
	peerSec, err := NewSecure()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p2p/pubkey" {
			_, _ = w.Write([]byte(`{"pubkey":"` + peerSec.PubKeyHex() + `"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New(Config{Encrypt: true, DialTimeout: 2e9, RequestTimeout: 5e9}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 僅測試 sealBody 路徑；模擬 Start 設置的 ctx（真實運行由 Start(ctx) 提供）。
	n.ctx = context.Background()

	plain := []byte(`{"node_id":"n1","height":1}`)
	out, err := n.sealBody(srv.URL+"/p2p/gossip", plain)
	if err != nil {
		t.Fatalf("sealBody 失敗: %v", err)
	}
	if bytes.Equal(out, plain) {
		t.Fatal("加密端應輸出加密 envelope，而非明文")
	}
	if !n.peerEncrypted(srv.URL) {
		t.Fatal("加密成功後 peerEncrypted 應為 true")
	}
	// 還原驗證內容一致。
	var env encEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("envelope 解析失敗: %v", err)
	}
	// 對端視角還原：peerSec 私鑰 × 發送端（n.Secure）公鑰 = 同一共享密鑰。
	key, err := peerSec.SharedKey(n.Secure.PubKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	iv, _ := hex.DecodeString(env.IV)
	data, _ := hex.DecodeString(env.Data)
	got, err := DecryptPayload(key, append(iv, data...))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("加解密不一致: got %q want %q", got, plain)
	}
}

// v65：openBody 收到明文（非加密 envelope）→ 原樣返回（相容舊節點）。
func TestOpenBodyPlainCompat(t *testing.T) {
	n, err := New(Config{Encrypt: true, DialTimeout: 2e9, RequestTimeout: 5e9}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 僅測試 openBody 相容路徑，不需 Start 循環。
	plain := []byte(`{"node_id":"old","height":9}`)
	req := httptest.NewRequest("POST", "/p2p/hello", bytes.NewReader(plain))
	out, err := n.openBody(req)
	if err != nil {
		t.Fatalf("openBody 不應拒絕明文（相容降級）: %v", err)
	}
	if string(out) != string(plain) {
		t.Fatalf("明文 body 應原樣返回，got %q", out)
	}
}
