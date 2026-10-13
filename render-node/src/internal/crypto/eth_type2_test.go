package crypto

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
)

// M76：EIP-1559 type 2 交易 roundtrip——編碼→解碼→payload→簽名→恢復→驗證。
func TestEthType2RoundTrip(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	tx := &EthTx{
		TxType:         2,
		ChainID:        big.NewInt(EthChainID),
		Nonce:          big.NewInt(7),
		MaxPriorityFee: big.NewInt(2_000_000_000), // 2 gwei
		MaxFee:         big.NewInt(50_000_000_000), // 50 gwei
		Gas:            big.NewInt(21000),
		To:             bytes20(0x11),
		Value:          big.NewInt(1_000_000_000_000_000_000), // 1 TACm wei
		Data:           []byte{0xde, 0xad, 0xbe, 0xef},
	}
	raw, err := SignEthRawTxType2(tx, priv)
	if err != nil {
		t.Fatalf("type2 簽名: %v", err)
	}
	if raw[0] != 0x02 {
		t.Fatalf("raw 需 0x02 前綴，got %x", raw[0])
	}
	dec, err := DecodeEthRawTx(raw)
	if err != nil {
		t.Fatalf("type2 解碼: %v", err)
	}
	if dec.TxType != 2 || dec.ChainID.Int64() != EthChainID || dec.Nonce.Int64() != 7 {
		t.Fatalf("type2 欄位錯誤: %+v", dec)
	}
	if dec.MaxPriorityFee.Int64() != 2_000_000_000 || dec.MaxFee.Int64() != 50_000_000_000 {
		t.Fatalf("type2 fee 欄位錯誤: prio=%v max=%v", dec.MaxPriorityFee, dec.MaxFee)
	}
	if dec.Gas.Int64() != 21000 || hex.EncodeToString(dec.To) != "1111111111111111111111111111111111111111" {
		t.Fatalf("type2 gas/to 錯誤: gas=%v to=%x", dec.Gas, dec.To)
	}
	// 簽名恢復應得到原私鑰公鑰。
	pub, err := RecoverEthSignerType2(dec.Payload(), dec.R, dec.S, dec.V)
	if err != nil {
		t.Fatalf("type2 恢復: %v", err)
	}
	if !pub.IsEqual(priv.PubKey()) {
		t.Fatal("type2 恢復公鑰不匹配")
	}
	// 重編碼一致性（簽名欄位不變）。
	re := EncodeEthRawTxType2(dec)
	if hex.EncodeToString(re) != hex.EncodeToString(raw) {
		t.Fatalf("type2 重編碼不一致:\n%s\n%s", hex.EncodeToString(re), hex.EncodeToString(raw))
	}
}

// 壞 chainId 的交易 payload 應與本鏈不同（重放防護）。
func TestEthType2ChainIDGuard(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	good := &EthTx{
		TxType: 2, ChainID: big.NewInt(EthChainID), Nonce: big.NewInt(0),
		MaxPriorityFee: big.NewInt(1), MaxFee: big.NewInt(2),
		Gas: big.NewInt(21000), To: bytes20(0x22), Value: big.NewInt(0),
	}
	bad := &EthTx{
		TxType: 2, ChainID: big.NewInt(EthChainID + 1), Nonce: big.NewInt(0),
		MaxPriorityFee: big.NewInt(1), MaxFee: big.NewInt(2),
		Gas: big.NewInt(21000), To: bytes20(0x22), Value: big.NewInt(0),
	}
	g, _ := SignEthRawTxType2(good, priv)
	b, _ := SignEthRawTxType2(bad, priv)
	if hex.EncodeToString(g) == hex.EncodeToString(b) {
		t.Fatal("不同 chainId 簽名 raw 不應相同")
	}
}

func bytes20(v byte) []byte {
	b := make([]byte, 20)
	for i := range b {
		b[i] = v
	}
	return b
}
