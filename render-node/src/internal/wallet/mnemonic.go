package wallet

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"strings"

	"tacm/internal/crypto"
)

//go:embed assets/bip39_english.txt
var bip39FS embed.FS

var (
	bip39Words   []string
	bip39Indexes map[string]int
)

func init() {
	raw, err := bip39FS.ReadFile("assets/bip39_english.txt")
	if err != nil {
		panic(fmt.Sprintf("wallet: 讀取 BIP39 字表失敗: %v", err))
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	bip39Words = make([]string, 0, len(lines))
	bip39Indexes = make(map[string]int, len(lines))
	for i, w := range lines {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		bip39Words = append(bip39Words, w)
		bip39Indexes[w] = i
	}
	if len(bip39Words) != 2048 {
		panic(fmt.Sprintf("wallet: BIP39 字表長度 = %d, want 2048", len(bip39Words)))
	}
}

// PrivateKeyToMnemonic 以私鑰（32 字節）為 entropy 編碼 BIP39 助記詞（24 詞，含 checksum）。
// 雙向可逆：助記詞即私鑰的完整備份，可直接還原 WIF 與地址。
func PrivateKeyToMnemonic(privateKey []byte) (string, error) {
	if len(privateKey) != 32 {
		return "", fmt.Errorf("wallet: 私鑰長度 = %d, want 32", len(privateKey))
	}
	entropy := make([]byte, 32)
	copy(entropy, privateKey)

	// checksum = SHA256(entropy) 前 ENT/32 bit（ENT=256 → 8 bit）
	sum := sha256.Sum256(entropy)
	bits := make([]byte, 0, 264)
	bits = appendBits(bits, entropy, 256)
	bits = appendBits(bits, sum[:1], 8)

	words := make([]string, 24)
	for i := 0; i < 24; i++ {
		idx := 0
		for b := 0; b < 11; b++ {
			idx = (idx << 1) | int(bits[i*11+b])
		}
		words[i] = bip39Words[idx]
	}
	return strings.Join(words, " "), nil
}

// MnemonicToPrivateKey 解析 BIP39 助記詞回私鑰（驗證字表 + checksum）。
func MnemonicToPrivateKey(mnemonic string) ([]byte, error) {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(mnemonic)))
	if len(words) != 24 {
		return nil, fmt.Errorf("wallet: 助記詞詞數 = %d, want 24", len(words))
	}
	bits := make([]byte, 0, 264)
	for _, w := range words {
		idx, ok := bip39Indexes[w]
		if !ok {
			return nil, fmt.Errorf("wallet: 助記詞含無效單詞 %q", w)
		}
		for b := 10; b >= 0; b-- { // 11 bits per word
			bits = append(bits, byte((idx>>b)&1))
		}
	}
	entropy := make([]byte, 32)
	byteOf := func(start int) byte {
		var v byte
		for i := 0; i < 8; i++ {
			v = (v << 1) | bits[start+i]
		}
		return v
	}
	for i := 0; i < 32; i++ {
		entropy[i] = byteOf(i * 8)
	}
	// checksum 驗證：最後 8 bit 必須等於 SHA256(entropy)[0] 前 8 bit
	var cs byte
	for i := 0; i < 8; i++ {
		cs = (cs << 1) | bits[256+i]
	}
	sum := sha256.Sum256(entropy)
	if cs != sum[0] {
		return nil, fmt.Errorf("wallet: 助記詞 checksum 不符（可能打錯字）")
	}
	return entropy, nil
}

// MnemonicToWIF 助記詞 → WIF（直接還原私鑰）。
func MnemonicToWIF(mnemonic string) (string, error) {
	priv, err := MnemonicToPrivateKey(mnemonic)
	if err != nil {
		return "", err
	}
	return crypto.PrivateKeyToWIF(priv)
}

// WIFToMnemonic WIF → 24 詞助記詞。
func WIFToMnemonic(wif string) (string, error) {
	priv, err := crypto.WIFToPrivateKey(wif)
	if err != nil {
		return "", err
	}
	return PrivateKeyToMnemonic(priv)
}

func appendBits(dst []byte, src []byte, n int) []byte {
	for i := 0; i < n; i++ {
		byteIdx := i / 8
		bitIdx := 7 - (i % 8)
		dst = append(dst, (src[byteIdx]>>bitIdx)&1)
	}
	return dst
}
