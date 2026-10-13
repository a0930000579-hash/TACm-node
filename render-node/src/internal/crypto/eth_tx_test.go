package crypto

import (
	"bytes"
	"math/big"
	"testing"
)

func TestEthSignRawTxRoundTrip(t *testing.T) {
	kp, err := EthKeyFromHex("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	tx := &EthTx{
		Nonce:    big.NewInt(7),
		GasPrice: big.NewInt(1000000000),
		Gas:      big.NewInt(21000),
		To:       make([]byte, 20),
		Value:    big.NewInt(123456789),
		Data:     []byte{0x01, 0x02, 0x03},
	}
	tx.To[0] = 0x11
	tx.To[19] = 0xaa
	raw, err := SignEthRawTx(tx, kp, EthChainID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEthRawTx(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nonce.Cmp(tx.Nonce) != 0 || got.Value.Cmp(tx.Value) != 0 ||
		got.GasPrice.Cmp(tx.GasPrice) != 0 || got.Gas.Cmp(tx.Gas) != 0 {
		t.Fatalf("欄位不符: %+v", got)
	}
	if !bytes.Equal(got.To, tx.To) || !bytes.Equal(got.Data, tx.Data) {
		t.Fatal("to/data 不符")
	}
	// EIP-155 v 檢查。
	wantV := int64(35 + 2*EthChainID)
	if got.V.Int64() < wantV || got.V.Int64() > wantV+3 {
		t.Fatalf("v=%v 不在 [%d,%d]", got.V, wantV, wantV+3)
	}
	// 恢復比對。
	pub, err := RecoverEthSigner(tx.Payload(), got.R, got.S, got.V, EthChainID)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.IsEqual(kp.PubKey()) {
		t.Fatal("恢復公鑰不符")
	}
}

func TestABISelectorKnown(t *testing.T) {
	// ERC-20 transfer(address,uint256) 的知名選擇器 0xa9059cbb。
	if got := ABISelector("transfer(address,uint256)"); got != [4]byte{0xa9, 0x05, 0x9c, 0xbb} {
		t.Fatalf("selector=%x 期望 a9059cbb", got)
	}
	if got := ABISelector("mint(address,uint256)"); got[0] == 0 && got[1] == 0 && got[2] == 0 && got[3] == 0 {
		t.Fatal("mint selector 不應全零")
	}
}

func TestABIEncodeAndDecode(t *testing.T) {
	sel := ABISelector("mint(address,uint256)")
	to := make([]byte, 20)
	to[0] = 0xde
	to[19] = 0xad
	calldata, err := ABIEncodeCall(sel, to, big.NewInt(1000))
	if err != nil {
		t.Fatal(err)
	}
	if len(calldata) != 4+64 {
		t.Fatalf("calldata 長度=%d 期望 68", len(calldata))
	}
	if !bytes.Equal(calldata[:4], sel[:]) {
		t.Fatal("selector 不符")
	}
	if !bytes.Equal(calldata[4+12:4+32], to) {
		t.Fatal("address 編碼不符")
	}
	if new(big.Int).SetBytes(calldata[36:68]).Cmp(big.NewInt(1000)) != 0 {
		t.Fatal("uint256 編碼不符")
	}
}

func TestABIDecodeResult(t *testing.T) {
	// totalSupply() → uint256。
	data := pad32(big.NewInt(777000))
	vals, err := ABIDecodeResult(data, []string{"uint256"})
	if err != nil {
		t.Fatal(err)
	}
	if vals[0].(*big.Int).Cmp(big.NewInt(777000)) != 0 {
		t.Fatalf("uint256=%v", vals[0])
	}
	// balanceOf(address) → 編碼 address。
	addr := make([]byte, 20)
	addr[5] = 0x42
	enc := append(make([]byte, 12), addr...)
	vals, err = ABIDecodeResult(enc, []string{"address"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(vals[0].([]byte), addr) {
		t.Fatal("address 解碼不符")
	}
	// string（動態）。
	enc = append(pad32(big.NewInt(32)), pad32(big.NewInt(5))...)
	enc = append(enc, []byte("hello")...)
	vals, err = ABIDecodeResult(enc, []string{"string"})
	if err != nil {
		t.Fatal(err)
	}
	if vals[0].(string) != "hello" {
		t.Fatalf("string=%v", vals[0])
	}
}

func TestEthAddressBytes(t *testing.T) {
	b, err := EthAddressBytes("0x" + "11" + "00" + "22" + "00000000000000000000000000000000aa")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 20 {
		t.Fatalf("len=%d", len(b))
	}
	if b[0] != 0x11 || b[1] != 0x00 || b[2] != 0x22 || b[19] != 0xaa {
		t.Fatalf("內容不符 %x", b)
	}
	if h := EthAddressHex(b); h != "0x11002200000000000000000000000000000000aa" {
		t.Fatalf("hex=%s", h)
	}
}
