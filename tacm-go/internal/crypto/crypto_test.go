package crypto

import (
	"encoding/hex"
	"strings"
	"testing"
)

// 固定私鑰（32 字節 0x01），所有期望向量由 Python tac_crypto 真實輸出生成。

func mustPriv(t *testing.T) []byte {
	t.Helper()
	pk, err := hex.DecodeString("0101010101010101010101010101010101010101010101010101010101010101")
	if err != nil {
		t.Fatalf("解碼固定私鑰失敗: %v", err)
	}
	return pk
}

func TestKeyPairFromPrivateKey(t *testing.T) {
	kp, err := KeyPairFromPrivateKey(mustPriv(t))
	if err != nil {
		t.Fatalf("從私鑰構造失敗: %v", err)
	}

	wantCompressed := "031b84c5567b126440995d3ed5aaba0565d71e1834604819ff9c17f5e9d5dd078f"
	if got := hex.EncodeToString(kp.PublicKeyCompressed()); got != wantCompressed {
		t.Errorf("壓縮公鑰不符\n got=%s\nwant=%s", got, wantCompressed)
	}

	wantUncompressed := "1b84c5567b126440995d3ed5aaba0565d71e1834604819ff9c17f5e9d5dd078f70beaf8f588b541507fed6a642c5ab42dfdf8120a7f639de5122d47a69a8e8d1"
	if got := hex.EncodeToString(kp.PublicKey()); got != wantUncompressed {
		t.Errorf("未壓縮公鑰不符\n got=%s\nwant=%s", got, wantUncompressed)
	}

	if got := hex.EncodeToString(kp.PrivateKey()); got != strings.Repeat("01", 32) {
		t.Errorf("私鑰往返不符, got=%s", got)
	}
}

func TestAddress(t *testing.T) {
	kp, _ := KeyPairFromPrivateKey(mustPriv(t))
	addr, err := kp.Address()
	if err != nil {
		t.Fatalf("派生地址失敗: %v", err)
	}
	want := "tx01C6Rc3w25VHud3dLDamutaqfKWqhrLRTaD"
	if addr != want {
		t.Errorf("地址不符\n got=%s\nwant=%s", addr, want)
	}
	if !IsValidAddress(addr) {
		t.Errorf("合法地址被判無效: %s", addr)
	}
	if !strings.HasPrefix(addr, "tx0") {
		t.Errorf("地址前綴應為 tx0: %s", addr)
	}
}

func TestInvalidAddresses(t *testing.T) {
	bad := []string{
		"",
		"tx0",
		"tacm1abc",
		"0x1234",
		"tx01C6Rc3w25VHud3dLDamutaqfKWqhrLRTaX", // 校驗和破壞
		"bc1qxyz",
	}
	for _, b := range bad {
		if IsValidAddress(b) {
			t.Errorf("非法地址被判合法: %q", b)
		}
	}
}

func TestWIFRoundtrip(t *testing.T) {
	kp, _ := KeyPairFromPrivateKey(mustPriv(t))
	wif, err := kp.WIF()
	if err != nil {
		t.Fatalf("WIF 導出失敗: %v", err)
	}
	wantWIF := "KwFfNUhSDaASSAwtG7ssQM1uVX8RgX5GHWnnLfhfiQDigjioWXHH"
	if wif != wantWIF {
		t.Errorf("WIF 不符\n got=%s\nwant=%s", wif, wantWIF)
	}
	recovered, err := KeyPairFromWIF(wif)
	if err != nil {
		t.Fatalf("從 WIF 恢復失敗: %v", err)
	}
	addr, _ := kp.Address()
	rAddr, _ := recovered.Address()
	if rAddr != addr {
		t.Errorf("WIF 往返地址不一致\n got=%s\nwant=%s", rAddr, addr)
	}
}

func fixedTx(t *testing.T, addr string) map[string]any {
	kp, _ := KeyPairFromPrivateKey(mustPriv(t))
	return map[string]any{
		"from":   addr,
		"to":     "tx0test1234567890abcdefghijklmnopqrst",
		"amount": "100.5",
		"fee":    "0.1",
		"ts":     1700000000,
		"nonce":  1,
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
	}
}

func TestTransactionSighash(t *testing.T) {
	kp, _ := KeyPairFromPrivateKey(mustPriv(t))
	addr, _ := kp.Address()
	tx := fixedTx(t, addr)
	sh, err := TxSighash(tx)
	if err != nil {
		t.Fatalf("sighash 失敗: %v", err)
	}
	want := "68c13fbdf65310e58f5a4aa6ae5c1c8c12b654c0c287b3c78584e1380607643f"
	if got := hex.EncodeToString(sh); got != want {
		t.Errorf("sighash 不符\n got=%s\nwant=%s", got, want)
	}
}

func TestTransactionSignAndVerify(t *testing.T) {
	kp, _ := KeyPairFromPrivateKey(mustPriv(t))
	addr, _ := kp.Address()
	tx := fixedTx(t, addr)

	sig, err := SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		t.Fatalf("簽名失敗: %v", err)
	}
	// Go 采用 BIP-146 low-s（交易延展性保護），與 Python ecdsa 的 high-s 簽名 hex 不同，
	// 但二者數學等價、互相可驗證。這裡驗證 DER 結構與有效性，而非字面 hex。
	if !strings.HasPrefix(sig, "30") || len(sig) < 10 {
		t.Errorf("簽名應為 DER（30 開頭）, got=%s", sig)
	}
	if !VerifyTransactionSignature(tx, sig, addr) {
		t.Errorf("合法簽名驗證失敗")
	}
	if !VerifyTransactionWithPublicKey(tx, sig, tx["pubkey"].(string)) {
		t.Errorf("公鑰驗證失敗")
	}

	// 篡改金額後簽名必須失效。
	tampered := cloneHeader(tx)
	tampered["amount"] = "999"
	if VerifyTransactionSignature(tampered, sig, addr) {
		t.Errorf("篡改後簽名應失效，但驗證通過")
	}

	// 缺少 pubkey 必須無法驗證。
	noPub := cloneHeader(tx)
	delete(noPub, "pubkey")
	if VerifyTransactionSignature(noPub, sig, addr) {
		t.Errorf("缺少 pubkey 時應驗證失敗")
	}
}

func TestBlockHash(t *testing.T) {
	header := map[string]any{
		"height": 1, "prev_hash": nil, "merkle_root": "abc",
		"proposer": "node1", "ts": 1700000000, "tx_count": 0,
	}
	h1, err := HashBlockHeader(header)
	if err != nil {
		t.Fatalf("區塊哈希失敗: %v", err)
	}
	h2, _ := HashBlockHeader(header)
	want := "a5c17fe84c3cfec6be4983ae411995da89e82768cb8f1a40c2325948311cec72"
	if h1 != want {
		t.Errorf("區塊哈希不符\n got=%s\nwant=%s", h1, want)
	}
	if h1 != h2 {
		t.Errorf("區塊哈希不確定: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("區塊哈希長度應為64, got=%d", len(h1))
	}
}

func TestMerkleRoot(t *testing.T) {
	items := []any{"tx1", "tx2", "tx3", "tx4"}
	root, err := MerkleRoot(items)
	if err != nil {
		t.Fatalf("Merkle 根失敗: %v", err)
	}
	want := "ea59a369466be42d1a4783f09ae0721a5a157d6dba9c4b053d407b5a4b9af145"
	if root != want {
		t.Errorf("Merkle 根不符\n got=%s\nwant=%s", root, want)
	}
	empty, _ := MerkleRoot(nil)
	wantEmpty := "2675ba3259b1db9fa081ade2d17dffc176ea91280f2be5b5f9dd4cecc6032bc8"
	if empty != wantEmpty {
		t.Errorf("空 Merkle 根不符\n got=%s\nwant=%s", empty, wantEmpty)
	}
}

func TestMerkleProof(t *testing.T) {
	items := []any{"tx1", "tx2", "tx3", "tx4"}
	p0, err := MerkleProof(items, 0)
	if err != nil {
		t.Fatalf("proof(0) 失敗: %v", err)
	}
	want0 := []string{
		"27ca64c092a959c7edc525ed45e845b1de6a7590d173fd2fad9133c8a779a1e3",
		"5709445d1034999688c7261a7c9cd07b521fcd02b97c71fb30ca85b9104487ca",
	}
	if !equalStrings(p0, want0) {
		t.Errorf("proof(0) 不符\n got=%v\nwant=%v", p0, want0)
	}

	p2, _ := MerkleProof(items, 2)
	want2 := []string{
		"41b637cfd9eb3e2f60f734f9ca44e5c1559c6f481d49d6ed6891f3e9a086ac78",
		"bbea820f07f7f89aeea1ab4a354ecea39f2f72accd05c64371522ee371cd0c48",
	}
	if !equalStrings(p2, want2) {
		t.Errorf("proof(2) 不符\n got=%v\nwant=%v", p2, want2)
	}

	// 奇數葉（自我複製）不應報錯。
	if _, err := MerkleProof([]any{"a", "b", "c"}, 1); err != nil {
		t.Errorf("奇數葉 proof 報錯: %v", err)
	}
}

func TestPoW(t *testing.T) {
	root, _ := MerkleRoot([]any{"tx1", "tx2", "tx3", "tx4"})
	header := map[string]any{
		"height": 1, "prev_hash": nil, "merkle_root": root,
		"proposer": "node1", "ts": 1700000000, "tx_count": 4,
	}
	res, err := MineBlock(header, 2, 100000)
	if err != nil {
		t.Fatalf("PoW 失敗: %v", err)
	}
	if res == nil {
		t.Fatal("PoW 未找到結果")
	}
	if res.Nonce != 214 {
		t.Errorf("PoW nonce 不符\n got=%d\nwant=214", res.Nonce)
	}
	wantHash := "00486e003dc2d2a01cfc14abab9d0094439f0f98f1bd1bcc49304bbb736d429c"
	if res.Hash != wantHash {
		t.Errorf("PoW hash 不符\n got=%s\nwant=%s", res.Hash, wantHash)
	}
	header["nonce"] = res.Nonce
	ok, err := VerifyPow(header, 2)
	if err != nil {
		t.Fatalf("VerifyPow 失敗: %v", err)
	}
	if !ok {
		t.Errorf("PoW 驗證失敗")
	}
}

func TestGenerateAndRecover(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("生成密鑰對失敗: %v", err)
	}
	addr, _ := kp.Address()
	if !IsValidAddress(addr) {
		t.Errorf("生成地址非法: %s", addr)
	}
	rec, err := KeyPairFromPrivateKey(kp.PrivateKey())
	if err != nil {
		t.Fatalf("恢復密鑰對失敗: %v", err)
	}
	rAddr, _ := rec.Address()
	if rAddr != addr {
		t.Errorf("生成→恢復地址不一致\n got=%s\nwant=%s", rAddr, addr)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
