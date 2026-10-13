package tacclient

import (
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestJSInteropSignature 驗證 JavaScript SDK（sdk/tacjs）產出的
// 地址與 DER 簽名能被 Go 端認證——證明跨語言互操作：
// 前端用 tacjs 簽章的交易，Go 節點可直接驗證入池。
func TestJSInteropSignature(t *testing.T) {
	// 固定私鑰 0x01*32 由 tacjs 產出（見 sdk/tacjs/test 冒煙輸出）。
	addr := "tx01C6Rc3w25VHud3dLDamutaqfKWqhrLRTaD"
	pub := "031b84c5567b126440995d3ed5aaba0565d71e1834604819ff9c17f5e9d5dd078f"
	sig := "3045022100dcac67ad3a396d6f67906913d699a3e2930512d17ae29d1cffdf191284bd909502205823aec9752036bd4d8b94bc17cccfebbe6bbcd7a76fcfa371ac38486e67a20c"

	// 私鑰推導地址與 JS 地址一致（再次證明 key 互通）。
	priv := make([]byte, 32)
	for i := range priv {
		priv[i] = 1
	}
	kp, err := crypto.KeyPairFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := kp.Address()
	if err != nil {
		t.Fatal(err)
	}
	if derived != addr {
		t.Fatalf("地址不一致: Go=%s JS=%s", derived, addr)
	}

	tx := map[string]any{
		"from": addr, "to": "tx0bob",
		"amount": "5", "fee": "0.1", "nonce": 0,
		"ts": int64(1700000000), "memo": "js-interop",
		"pubkey": pub, "signature": sig,
	}
	if !crypto.VerifyTransactionSignature(tx, sig, addr) {
		t.Fatal("JS 簽名未通過 Go 驗證")
	}

	// nonce 數值型別（int64 vs JSON number）不影響 sighash：字面量相同。
	_ = time.Now()
}
