package wallet

import (
	"strings"
	"testing"

	"tacm/internal/crypto"
)

func generateTestKey() (*crypto.KeyPair, error) { return crypto.GenerateKeyPair() }

// TestMnemonicRoundtrip：WIF → 24 詞 → 還原 WIF/私鑰完全一致。
func TestMnemonicRoundtrip(t *testing.T) {
	k, err := generateTestKey()
	if err != nil {
		t.Fatal(err)
	}
	wif, err := k.WIF()
	if err != nil {
		t.Fatal(err)
	}
	m, err := WIFToMnemonic(wif)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Fields(m) == nil || len(strings.Fields(m)) != 24 {
		t.Fatalf("助記詞詞數錯誤: %q", m)
	}
	wif2, err := MnemonicToWIF(m)
	if err != nil {
		t.Fatalf("還原失敗: %v", err)
	}
	if wif2 != wif {
		t.Errorf("還原 WIF 不一致:\n got %s\nwant %s", wif2, wif)
	}
}

// TestMnemonicChecksum：篡改任一單詞 → checksum 報錯。
func TestMnemonicChecksum(t *testing.T) {
	k, _ := generateTestKey()
	wif, _ := k.WIF()
	m, _ := WIFToMnemonic(wif)
	words := strings.Fields(m)
	words[0] = "zoo" // 必然不等於原詞
	if _, err := MnemonicToWIF(strings.Join(words, " ")); err == nil {
		t.Error("篡改助記詞應報 checksum 錯誤")
	}
	// 24 詞以外拒絕
	if _, err := MnemonicToWIF("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon"); err == nil {
		t.Error("23 詞應報錯")
	}
}

// TestGenerateMnemonic：v58 標準 BIP-39 生成——12/24 詞數、checksum 有效、256bit 可還原。
func TestGenerateMnemonic(t *testing.T) {
	// 12 詞（128 bit）＋24 詞（256 bit）。
	m12, err := GenerateMnemonic(128)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(m12)); n != 12 {
		t.Fatalf("128bit 詞數 = %d, want 12", n)
	}
	m24, err := GenerateMnemonic(256)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(m24)); n != 24 {
		t.Fatalf("256bit 詞數 = %d, want 24", n)
	}
	// 256bit roundtrip：MnemonicToPrivateKey 可還原（entropy＝32B 私鑰）。
	priv, err := MnemonicToPrivateKey(m24)
	if err != nil {
		t.Fatalf("MnemonicToPrivateKey: %v", err)
	}
	if len(priv) != 32 {
		t.Fatalf("還原私鑰長度 = %d", len(priv))
	}
	// 非法位數拒絕。
	for _, b := range []int{100, 128 + 16, 300} {
		if _, err := GenerateMnemonic(b); err == nil {
			t.Fatalf("entropyBits=%d 應報錯", b)
		}
	}
	// 12 詞同樣可作標準 BIP-39 seed 來源（HD 派生不報錯）。
	if _, err := crypto.MnemonicToSeed(m12, ""); err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}
}
