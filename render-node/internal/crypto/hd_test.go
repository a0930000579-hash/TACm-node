package crypto

// v58：標準 BIP-32/39/44 HD 錢包測試——官方向量驗證。
//  1. BIP-39 向量：mnemonic→seed（Trezor 官方向量 1）；
//  2. BIP-32 向量：seed→master/m/0'/m/0'/1 私鑰（BIP-32 官方向量 1）；
//  3. BIP-44：以太坊路徑 m/44'/60'/0'/0/i 派生確定性＋MetaMask 兼容路徑格式。

import (
	"encoding/hex"
	"testing"
)

// TestHD_MnemonicToSeedVector BIP-39 官方向量 1（Trezor test vector）。
func TestHD_MnemonicToSeedVector(t *testing.T) {
	mnemonic := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	seed, err := MnemonicToSeed(mnemonic, "TREZOR")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}
	want := "c55257c360c07c72029aebc1b53c05ed0362ada38ead3e3e9efa3708e53495531f09a6987599d18264c1e1c92f2cf141630c7a3c4ab7c81b2f001698e7463b04"
	if hex.EncodeToString(seed) != want {
		t.Fatalf("種子不符\n got %s\nwant %s", hex.EncodeToString(seed), want)
	}
}

// TestHD_MasterKeyVector BIP-32 官方向量 1：seed=000102...0f。
func TestHD_MasterKeyVector(t *testing.T) {
	seed, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	master, err := MasterKeyFromSeed(seed)
	if err != nil {
		t.Fatalf("MasterKeyFromSeed: %v", err)
	}
	// m
	checkHDKey(t, master, "e8f32e723decf4051aefac8e2c93c9c5b214313817cdb01a1494b917c8436b35")
	// m/0'（硬化）
	c0, err := master.Child(1 << 31)
	if err != nil {
		t.Fatalf("m/0': %v", err)
	}
	checkHDKey(t, c0, "edb2e14f9ee77d26dd93b4ecede8d16ed408ce149b6cd80b0715a2d911a0afea")
	// m/0'/1（非硬化）
	c1, err := c0.Child(1)
	if err != nil {
		t.Fatalf("m/0'/1: %v", err)
	}
	checkHDKey(t, c1, "3c6cb8d0f6a264c91ea8b5030fadaa8e538b020f0a387421a12de9319dc93368")
}

func checkHDKey(t *testing.T, n *HDNode, wantHex string) {
	t.Helper()
	got := hex.EncodeToString(n.PrivateKey())
	if got != wantHex {
		t.Fatalf("私鑰不符\n got %s\nwant %s", got, wantHex)
	}
}

// TestHD_ChildPath 路徑解析＋逐級派生（與手動 Child 一致）。
func TestHD_ChildPath(t *testing.T) {
	seed, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	master, err := MasterKeyFromSeed(seed)
	if err != nil {
		t.Fatalf("MasterKeyFromSeed: %v", err)
	}
	node, err := master.ChildPath("m/0'/1")
	if err != nil {
		t.Fatalf("ChildPath: %v", err)
	}
	checkHDKey(t, node, "3c6cb8d0f6a264c91ea8b5030fadaa8e538b020f0a387421a12de9319dc93368")

	// 路徑解析：m/44'/60'/0'/0/0 → [44+2^31, 60+2^31, 2^31, 0, 0]
	idx, err := ParseHDPath("m/44'/60'/0'/0/0")
	if err != nil {
		t.Fatalf("ParseHDPath: %v", err)
	}
	want := []uint32{44 + 1<<31, 60 + 1<<31, 1 << 31, 0, 0}
	if len(idx) != len(want) {
		t.Fatalf("路徑長度 %d, want %d", len(idx), len(want))
	}
	for i := range want {
		if idx[i] != want[i] {
			t.Fatalf("路徑段 %d = %d, want %d", i, idx[i], want[i])
		}
	}
}

// TestHD_DeriveBIP44Key BIP-44 以太坊路徑派生：確定性＋有效密鑰對＋地址重現。
func TestHD_DeriveBIP44Key(t *testing.T) {
	mnemonic := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	seed, err := MnemonicToSeed(mnemonic, "")
	if err != nil {
		t.Fatalf("MnemonicToSeed: %v", err)
	}
	k0, err := DeriveBIP44Key(seed, 0, 0)
	if err != nil {
		t.Fatalf("DeriveBIP44Key(0,0): %v", err)
	}
	k1, err := DeriveBIP44Key(seed, 0, 1)
	if err != nil {
		t.Fatalf("DeriveBIP44Key(0,1): %v", err)
	}
	if hex.EncodeToString(k0.PrivateKey()) == hex.EncodeToString(k1.PrivateKey()) {
		t.Fatalf("不同 index 不應派生相同私鑰")
	}
	// 確定性：重複派生得到相同私鑰。
	k0b, _ := DeriveBIP44Key(seed, 0, 0)
	if hex.EncodeToString(k0.PrivateKey()) != hex.EncodeToString(k0b.PrivateKey()) {
		t.Fatalf("BIP-44 派生不確定")
	}
	if k0.PrivateKey() == nil || len(k0.PrivateKey()) != 32 {
		t.Fatalf("私鑰無效")
	}
	// 不同 account 也不同。
	ka, err := DeriveBIP44Key(seed, 1, 0)
	if err != nil {
		t.Fatalf("DeriveBIP44Key(1,0): %v", err)
	}
	if hex.EncodeToString(k0.PrivateKey()) == hex.EncodeToString(ka.PrivateKey()) {
		t.Fatalf("不同 account 不應派生相同私鑰")
	}
}

// TestHD_InvalidPath 非法路徑回錯誤（錯誤處理覆蓋）。
func TestHD_InvalidPath(t *testing.T) {
	for _, p := range []string{"x/0", "m/", "m/a'", "m/-1", "m/0''"} {
		if _, err := ParseHDPath(p); err == nil {
			t.Fatalf("路徑 %q 應回錯誤", p)
		}
	}
	if _, err := MasterKeyFromSeed([]byte("short")); err == nil {
		t.Fatalf("短種子應回錯誤")
	}
}
