package crypto

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// derToRS 解析 DER ECDSA 簽名的 R/S（測試輔助，僅處理標準 2-INTEGER）。
func derToRS(der []byte) (*big.Int, *big.Int) {
	i := 2
	readInt := func() *big.Int {
		if i+2 > len(der) || der[i] != 0x02 {
			panic("bad der")
		}
		l := int(der[i+1])
		v := new(big.Int).SetBytes(der[i+2 : i+2+l])
		i += 2 + l
		return v
	}
	return readInt(), readInt()
}

func signPayload(payload []byte, key *btcec.PrivateKey) (*big.Int, *big.Int, byte, *btcec.PublicKey) {
	digest := Keccak256(payload)
	sig := ecdsa.Sign(key, digest)
	r, s := derToRS(sig.Serialize())
	// 用 RecoverCompact 找 recid（測試用）：試 0..3 恢復比對公鑰。
	expected := key.PubKey()
	var recid byte
	for rc := byte(0); rc < 4; rc++ {
		compact := make([]byte, 65)
		compact[0] = 27 + rc
		r.FillBytes(compact[1:33])
		s.FillBytes(compact[33:65])
		pub, _, err := ecdsa.RecoverCompact(compact, digest)
		if err == nil && pub.IsEqual(expected) {
			recid = rc
			break
		}
	}
	return r, s, 27 + recid, expected
}

func TestEthRLPRoundTrip(t *testing.T) {
	to := bytes.Repeat([]byte{0x11}, 20)
	raw := rlpEncodeList(
		rlpEncodeInt(big.NewInt(7)),
		rlpEncodeInt(big.NewInt(1_000_000_000)),
		rlpEncodeInt(big.NewInt(21000)),
		rlpEncodeBytes(to),
		rlpEncodeInt(big.NewInt(500)),
		rlpEncodeBytes([]byte{0xde, 0xad}),
		rlpEncodeInt(big.NewInt(27)), rlpEncodeInt(big.NewInt(1)), rlpEncodeInt(big.NewInt(2)))
	payloadWant := rlpEncodeList(
		rlpEncodeInt(big.NewInt(7)),
		rlpEncodeInt(big.NewInt(1_000_000_000)),
		rlpEncodeInt(big.NewInt(21000)),
		rlpEncodeBytes(to),
		rlpEncodeInt(big.NewInt(500)),
		rlpEncodeBytes([]byte{0xde, 0xad}),
	)

	tx, err := DecodeEthRawTx(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tx.Nonce.Int64() != 7 || tx.GasPrice.Int64() != 1_000_000_000 || tx.Gas.Int64() != 21000 {
		t.Fatalf("欄位不符: %v %v %v", tx.Nonce, tx.GasPrice, tx.Gas)
	}
	if tx.Value.Int64() != 500 || !bytes.Equal(tx.To, bytes.Repeat([]byte{0x11}, 20)) || !bytes.Equal(tx.Data, []byte{0xde, 0xad}) {
		t.Fatalf("欄位不符: %v %x %x", tx.Value, tx.To, tx.Data)
	}
	// Payload 重建須與原 6 欄 RLP 一致。
	if !bytes.Equal(tx.Payload(), payloadWant) {
		t.Fatalf("payload 重建不符")
	}
	// 空 to = 合約建立（解碼不報錯）。
	rawCreate := rlpEncodeList(
		rlpEncodeInt(big.NewInt(0)), rlpEncodeInt(big.NewInt(1)), rlpEncodeInt(big.NewInt(1)),
		rlpEncodeBytes(nil), rlpEncodeInt(big.NewInt(0)), rlpEncodeBytes(nil),
		rlpEncodeInt(big.NewInt(27)), rlpEncodeInt(big.NewInt(1)), rlpEncodeInt(big.NewInt(2)))
	tx2, err := DecodeEthRawTx(rawCreate)
	if err != nil || len(tx2.To) != 0 {
		t.Fatalf("合約建立解碼: %v", err)
	}
}

func TestEthSignatureRecover(t *testing.T) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("tac-eth-payload-v1")
	r, s, v, pub := signPayload(payload, key)

	recovered, err := RecoverEthSigner(payload, r, s, big.NewInt(int64(v)), 0)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !recovered.IsEqual(pub) {
		t.Fatal("恢復公鑰不符")
	}
	if !VerifyEthSigByRecover(pub, payload, r, s, v) {
		t.Fatal("簽名驗證失敗")
	}
	// 篡改 payload 應失敗。
	if VerifyEthSigByRecover(pub, append(payload, 0x01), r, s, v) {
		t.Fatal("篡改 payload 仍通過")
	}
	// EIP-155 v 格式（chainId 1337）。
	v155 := 35 + 2*EthChainID + int(v-27)
	if !VerifyEthSigByRecover(pub, payload, r, s, byte(v155)) {
		t.Fatal("EIP-155 v 驗證失敗")
	}
}

func TestVerifyEthTx(t *testing.T) {
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("eth-payload-hex-01")
	r, s, v, pub := signPayload(payload, key)
	sigBytes := make([]byte, 65)
	r.FillBytes(sigBytes[:32])
	s.FillBytes(sigBytes[32:64])
	sigBytes[64] = v - 27

	fromHash := Hash160(pub.SerializeCompressed())
	from := Hash160ToAddress(fromHash)
	tx := map[string]any{
		"from":      from,
		"to":        from,
		"amount":    "100",
		"fee":       "21000",
		"memo":      "",
		"pubkey":    "eth:" + hex.EncodeToString(payload) + ":" + hex.EncodeToString(pub.SerializeCompressed()),
		"signature": "eth:" + hex.EncodeToString(sigBytes),
	}
	if !VerifyEthTx(tx) {
		t.Fatal("VerifyEthTx 應通過")
	}
	tx["from"] = Hash160ToAddress(Hash160([]byte("other")))
	if VerifyEthTx(tx) {
		t.Fatal("篡改 from 仍通過")
	}
}
