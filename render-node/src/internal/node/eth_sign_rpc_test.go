package node

import (
	"encoding/hex"
	"testing"

	"tacm/internal/crypto"
)

// signRPCMailTypedData 為 EIP-712 官方 Mail 範例（與 crypto 向量測試同源）。
const signRPCMailTypedData = `{
  "types": {
    "EIP712Domain": [
      {"name": "name", "type": "string"},
      {"name": "version", "type": "string"},
      {"name": "chainId", "type": "uint256"},
      {"name": "verifyingContract", "type": "address"}
    ],
    "Person": [
      {"name": "name", "type": "string"},
      {"name": "wallet", "type": "address"}
    ],
    "Mail": [
      {"name": "from", "type": "Person"},
      {"name": "to", "type": "Person"},
      {"name": "contents", "type": "string"}
    ]
  },
  "primaryType": "Mail",
  "domain": {
    "name": "Ether Mail",
    "version": "1",
    "chainId": 1,
    "verifyingContract": "0xCcCCccccCCCCcCCCCCCcCcCccCcCCCcCcccccccC"
  },
  "message": {
    "from": {"name": "Cow", "wallet": "0xCD2a3d9F938E13CD947Ec05AbC7FE734Df8DD826"},
    "to": {"name": "Bob", "wallet": "0xbBbBBBBbbBBBbbbBbbBbbbbBBbBbbbbBbBbbBBbB"},
    "contents": "Hello, Bob!"
  }
}`

// 本檔驗證 M136 錢包簽名 RPC（personal_sign / eth_sign / eth_signTypedData_v4
// / eth_ecRecover / personal_ecRecover）：簽名→恢復閉環、v 格式、typed data 路徑。

func TestSignRPC(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)
	nodeAddr, err := n.keypair.EthAddress()
	if err != nil {
		t.Fatal(err)
	}

	msg := "0x68656c6c6f" // "hello"

	// 1. personal_sign → 65 bytes，v=27/28。
	sigHex, ok := ethResult(t, srv, "personal_sign", nodeAddr, msg).(string)
	if !ok || len(sigHex) != 2+130 {
		t.Fatalf("personal_sign 結果異常: %v", sigHex)
	}
	sig, _ := hex.DecodeString(sigHex[2:])
	if len(sig) != 65 || (sig[0] != 27 && sig[0] != 28) {
		t.Fatalf("personal_sign v 異常: %x", sig[:1])
	}

	// 2. personal_ecRecover 閉環 → 節點地址。
	rec := ethResult(t, srv, "personal_ecRecover", msg, sigHex)
	if rec != nodeAddr {
		t.Fatalf("personal_ecRecover 不符: got %v want %s", rec, nodeAddr)
	}

	// 3. eth_sign（keccak 語義）→ eth_ecRecover 閉環。
	ethSig := ethResult(t, srv, "eth_sign", nodeAddr, msg).(string)
	rec2 := ethResult(t, srv, "eth_ecRecover", msg, ethSig)
	if rec2 != nodeAddr {
		t.Fatalf("eth_ecRecover 不符: got %v want %s", rec2, nodeAddr)
	}

	// 4. eth_signTypedData_v4（EIP-712）→ 65 bytes 且可恢復為節點地址。
	typedSig := ethResult(t, srv, "eth_signTypedData_v4", nodeAddr, signRPCMailTypedData).(string)
	if len(typedSig) != 2+130 {
		t.Fatalf("eth_signTypedData_v4 長度異常: %s", typedSig)
	}
	hash, err := crypto.HashTypedDataV4([]byte(signRPCMailTypedData))
	if err != nil {
		t.Fatal(err)
	}
	tsig, _ := hex.DecodeString(typedSig[2:])
	recovered, err := crypto.RecoverSignerAddressFromSig(hash, tsig)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != nodeAddr {
		t.Fatalf("typed data 簽名者不符: got %s want %s", recovered, nodeAddr)
	}

	// 5. 錯誤輸入：錯帳戶 / 短簽名 / 壞 JSON。
	if out := ethRPC(t, srv, "personal_sign", "0x0000000000000000000000000000000000000000", msg); out["error"] == nil {
		t.Fatal("錯帳戶應報錯")
	}
	if out := ethRPC(t, srv, "personal_ecRecover", msg, "0x1234"); out["error"] == nil {
		t.Fatal("短簽名應報錯")
	}
	if out := ethRPC(t, srv, "eth_signTypedData_v4", nodeAddr, "{}"); out["error"] == nil {
		t.Fatal("壞 typed data 應報錯")
	}
}

func TestSignRPCRoundTripEmpty(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)
	nodeAddr, _ := n.keypair.EthAddress()
	// 空訊息簽名閉環。
	sig := ethResult(t, srv, "personal_sign", nodeAddr, "0x").(string)
	rec := ethResult(t, srv, "personal_ecRecover", "0x", sig)
	if rec != nodeAddr {
		t.Fatalf("空訊息恢復不符: %v", rec)
	}
}
