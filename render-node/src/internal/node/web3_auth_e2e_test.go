package node

// EIP-4361（SIWE）錢包簽名登入端對端測試。
// 流程：nonce 挑戰 → EIP-191 personal_sign → verify → session；防重放 + 篡改地址應失敗。

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"

	"tacm/internal/crypto"
)

// SIWE 路由已由 NewRPCServer 於 rpc.go 統一註冊，直接使用標準測試 harness。

// ethKey 產生一組測試用以太金鑰與 0x 地址。
func ethKey(t *testing.T) (priv []byte, addr string) {
	t.Helper()
	k, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := k.PubKey().SerializeUncompressed()
	d := crypto.Keccak256(pub[1:])
	return k.Serialize(), crypto.EthAddressHex(d[len(d)-20:])
}

func TestWeb3SIWEHappyPathAndReplay(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	base := srv.URL

	priv, addr := ethKey(t)

	// 1) 取挑戰。
	nc := postJSON(t, base+"/api/auth/web3/nonce", map[string]any{"address": addr, "chain_id": "1"})
	if ok, _ := nc["ok"].(bool); !ok {
		t.Fatalf("nonce 失敗: %v", nc)
	}
	message, _ := nc["message"].(string)
	if message == "" {
		t.Fatal("nonce 未回傳 message")
	}

	// 2) EIP-191 personal_sign。
	sig, err := crypto.SignPersonalMessage([]byte(message), priv)
	if err != nil {
		t.Fatal(err)
	}
	sigHex := "0x" + hex.EncodeToString(sig)

	// 3) verify 成功。
	vf := postJSON(t, base+"/api/auth/web3/verify", map[string]any{
		"address":   addr,
		"message":   message,
		"signature": sigHex,
	})
	if ok, _ := vf["ok"].(bool); !ok {
		t.Fatalf("verify 失敗: %v", vf)
	}
	token, _ := vf["token"].(string)
	if token == "" {
		t.Fatal("verify 未回傳 token")
	}
	user, _ := vf["user"].(map[string]any)
	gotAddr, _ := user["wallet_addr"].(string)
	if !strings.EqualFold(gotAddr, addr) {
		t.Fatalf("user 地址不符: got %q want %q", gotAddr, addr)
	}

	// 4) 重放：同一 nonce/簽名再 verify 一次應失敗。
	replay := postJSON(t, base+"/api/auth/web3/verify", map[string]any{
		"address":   addr,
		"message":   message,
		"signature": sigHex,
	})
	if ok, _ := replay["ok"].(bool); ok {
		t.Fatal("重放同一簽名應失敗，卻成功")
	}
}

func TestWeb3SIWETamperedAddress(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	base := srv.URL

	priv, addr := ethKey(t)

	nc := postJSON(t, base+"/api/auth/web3/nonce", map[string]any{"address": addr})
	message, _ := nc["message"].(string)
	sig, _ := crypto.SignPersonalMessage([]byte(message), priv)
	sigHex := "0x" + hex.EncodeToString(sig)

	// 用另一個隨機地址冒充，應失敗。
	_, fakeAddr := ethKey(t)
	vf := postJSON(t, base+"/api/auth/web3/verify", map[string]any{
		"address":   fakeAddr,
		"message":   message,
		"signature": sigHex,
	})
	if ok, _ := vf["ok"].(bool); ok {
		t.Fatal("篡改地址應失敗，卻成功")
	}
}
