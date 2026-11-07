package node

import (
	"encoding/hex"
	"testing"

	"tacm/internal/crypto"
)

// v133：personal_importRawKey（記憶體金鑰匯入）＋debug_getBadBlocks（主流 RPC 補齊）。

func TestV133ImportRawKeyRPC(t *testing.T) {
	_, srv, _ := contractStudioSetup(t)
	// 私鑰 0x01（1 byte → 左補零 32 bytes）。
	priv := make([]byte, 32)
	priv[31] = 1
	kp, err := crypto.KeyPairFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	wantAddr, err := kp.EthAddress()
	if err != nil {
		t.Fatal(err)
	}

	hexKey := hex.EncodeToString(priv)
	got := ethResult(t, srv, "personal_importRawKey", "0x"+hexKey)
	if got != wantAddr {
		t.Fatalf("importRawKey 回傳地址不符: got %v want %s", got, wantAddr)
	}

	// 匯入後可用該地址 personal_sign → personal_ecRecover 閉環。
	msg := "0x68656c6c6f"
	sig := ethResult(t, srv, "personal_sign", wantAddr, msg).(string)
	if len(sig) != 2+130 {
		t.Fatalf("匯入金鑰簽名長度異常: %s", sig)
	}
	rec := ethResult(t, srv, "personal_ecRecover", msg, sig)
	if rec != wantAddr {
		t.Fatalf("匯入金鑰簽名恢復不符: got %v want %s", rec, wantAddr)
	}

	// 錯誤輸入：非 64 hex。
	if out := ethRPC(t, srv, "personal_importRawKey", "0x1234"); out["error"] == nil {
		t.Fatal("短私鑰應報錯")
	}
}

func TestV133DebugBadBlocksRPC(t *testing.T) {
	_, srv, _ := contractStudioSetup(t)
	arr, ok := ethResult(t, srv, "debug_getBadBlocks").([]any)
	if !ok {
		t.Fatalf("debug_getBadBlocks 應回陣列: %v", arr)
	}
	if len(arr) != 0 {
		t.Fatalf("本鏈無 bad block，應回空陣列: %v", arr)
	}
}
