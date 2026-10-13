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
