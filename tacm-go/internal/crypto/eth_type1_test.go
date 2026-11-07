package crypto

import (
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// TestEthType1SignRecover EIP-2930 type-1 交易：簽名 → raw → 解碼 → Payload → 恢復簽名者。
func TestEthType1SignRecover(t *testing.T) {
	chainID := int64(EthChainID)
	nonce := big.NewInt(5)
	gasPrice := big.NewInt(2_000_000_000)
	gas := big.NewInt(21000)
	to := make([]byte, 20)
	for i := range to {
		to[i] = byte(i)
	}
	value := big.NewInt(1_000_000)
	data := []byte{0xde, 0xad, 0xbe, 0xef}
	// accessList：一項 [to, [slot0, slot1]]
	alBody := rlpEncodeList(
		rlpEncodeBytes(to),
		rlpEncodeList(rlpEncodeInt(big.NewInt(0)), rlpEncodeInt(big.NewInt(1))),
	)
	alRaw := rlpEncodeList(alBody)

	tx := &EthTx{
		TxType:     1,
		ChainID:    big.NewInt(chainID),
		Nonce:      nonce,
		GasPrice:   gasPrice,
		Gas:        gas,
		To:         to,
		Value:      value,
		Data:       data,
		AccessList: alRaw,
	}
	payload := tx.Payload()
	if payload[0] != 0x01 {
		t.Fatalf("type1 payload 首字節應為 0x01, got %x", payload[0])
	}

	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	digest := Keccak256(payload)
	// SignCompact 的 65B 輸出自帶 recid（首字節 27+recid），避免 yParity 猜錯。
	compact, cerr := ecdsa.SignCompact(priv, digest, true)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if len(compact) != 65 {
		t.Fatalf("compact 簽名長度 %d, want 65", len(compact))
	}
	r := new(big.Int).SetBytes(compact[1:33])
	s := new(big.Int).SetBytes(compact[33:65])
	// compressed=true 時 header = 31+recid，故 recid = header-31。
	yParity := big.NewInt(int64(compact[0] - 31))

	raw := append([]byte{0x01}, rlpEncodeList(
		rlpEncodeInt(big.NewInt(chainID)),
		rlpEncodeInt(nonce),
		rlpEncodeInt(gasPrice),
		rlpEncodeInt(gas),
		rlpEncodeBytes(to),
		rlpEncodeInt(value),
		rlpEncodeBytes(data),
		alRaw,
		rlpEncodeInt(yParity),
		rlpEncodeInt(r),
		rlpEncodeInt(s),
	)...)

	dec, err := DecodeEthRawTx(raw)
	if err != nil {
		t.Fatalf("type1 解碼失敗: %v", err)
	}
	if dec.TxType != 1 {
		t.Fatalf("TxType = %d, want 1", dec.TxType)
	}
	if dec.ChainID.Int64() != chainID {
		t.Fatalf("chainId = %v, want %d", dec.ChainID, chainID)
	}
	if dec.Nonce.Int64() != 5 || dec.GasPrice.Int64() != gasPrice.Int64() {
		t.Fatalf("nonce/gasPrice 解碼不符: %v/%v", dec.Nonce, dec.GasPrice)
	}
	if len(dec.AccessList) == 0 {
		t.Fatal("accessList 應被保留")
	}
	// Payload 重建必須等於原簽名輸入
	if string(dec.Payload()) != string(payload) {
		t.Fatal("type1 Payload 重建與原簽名輸入不一致")
	}
	pub, err := RecoverEthSignerType2(dec.Payload(), dec.R, dec.S, dec.V)
	if err != nil {
		t.Fatalf("type1 簽名恢復失敗: %v", err)
	}
	if pub.X().Cmp(priv.PubKey().X()) != 0 || pub.Y().Cmp(priv.PubKey().Y()) != 0 {
		t.Fatal("恢復的簽名者公鑰不符")
	}
}
