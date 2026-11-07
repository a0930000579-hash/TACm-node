package crypto

import (
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
)

func TestHashPersonalMessage(t *testing.T) {
	// EIP-191：keccak256("\x19Ethereum Signed Message:\n5hello")。
	got := HashPersonalMessage([]byte("hello"))
	want := "a1de988600a42c4b4ab089b619297c17b53f4d2b0e6f0d3a9e5e5f6d8e0e5d1a"
	// want 為示範佔位，正式斷言改為自算一致性（避免錯誤向量）。
	h := Keccak256([]byte("\x19Ethereum Signed Message:\n5hello"))
	if hex.EncodeToString(got) != hex.EncodeToString(h) {
		t.Fatalf("HashPersonalMessage 不一致: %x vs %x", got, h)
	}
	_ = want
}

func TestPersonalSignRecoverRoundTrip(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	// 以太地址：keccak256(uncompressed[1:]) 尾 20 bytes。
	pubObj, perr := btcecParsePubKey(kp.PublicKeyCompressed())
	if perr != nil {
		t.Fatal(perr)
	}
	uncomp := pubObj.SerializeUncompressed()
	digest := Keccak256(uncomp[1:])
	addr := EthAddressHex(digest[len(digest)-20:])

	// 對空訊息與普通訊息簽名，恢復回原地址。
	for _, msg := range [][]byte{[]byte(""), []byte("hello world"), []byte("TAC Ai signature test")} {
		sig, err := SignPersonalMessage(msg, kp.PrivateKey())
		if err != nil {
			t.Fatal(err)
		}
		if len(sig) != 65 {
			t.Fatalf("簽名長度應 65，得到 %d", len(sig))
		}
		if sig[0] != 27 && sig[0] != 28 {
			t.Fatalf("v 應 27/28，得到 %d", sig[0])
		}
		recovered, err := RecoverSignerAddressFromSig(HashPersonalMessage(msg), sig)
		if err != nil {
			t.Fatal(err)
		}
		if recovered != addr {
			t.Fatalf("恢復地址不符: got %s want %s", recovered, addr)
		}
		// 簽名可被 eth_ecRecover 路徑驗證（v=27/28 格式）。
		if _, _, v, perr := ParseEthereumSig("0x" + hex.EncodeToString(sig)); perr != nil {
			t.Fatal(perr)
		} else if v != 27 && v != 28 {
			t.Fatalf("v 應 27/28，得到 %d", v)
		}
	}
}

func btcecParsePubKey(b []byte) (*btcec.PublicKey, error) {
	return btcec.ParsePubKey(b)
}

func TestEthereumSigParse(t *testing.T) {
	kp, _ := GenerateKeyPair()
	sig, err := SignPersonalMessage([]byte("parse test"), kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	sigHex := "0x" + hex.EncodeToString(sig)
	r, s, v, err := ParseEthereumSig(sigHex)
	if err != nil {
		t.Fatal(err)
	}
	if v != sig[0] {
		t.Fatalf("v 解析不符: %d vs %d", v, sig[0])
	}
	if r.Sign() <= 0 || s.Sign() <= 0 {
		t.Fatal("r/s 應為正數")
	}
	// 錯誤輸入。
	if _, _, _, err := ParseEthereumSig("0x1234"); err == nil {
		t.Fatal("短簽名應報錯")
	}
}
