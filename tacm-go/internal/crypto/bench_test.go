package crypto

import (
	"encoding/hex"
	"testing"
)

// M39：效能基準測試 — 提供 TAC 鏈吞吐證據（簽名/驗證/雜湊/Merkle）。
// 執行：go test ./internal/crypto/ -bench . -benchmem -run '^$'

func BenchmarkSHA256(b *testing.B) {
	data := []byte("TAC autonomous chain benchmark payload 2026")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SHA256(data)
	}
}

func BenchmarkSignDigest(b *testing.B) {
	kp, err := GenerateKeyPair()
	if err != nil {
		b.Fatal(err)
	}
	digest := SHA256([]byte("tx-benchmark-digest"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SignDigest(digest, kp.PrivateKey()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyDigest(b *testing.B) {
	kp, err := GenerateKeyPair()
	if err != nil {
		b.Fatal(err)
	}
	digest := SHA256([]byte("tx-benchmark-digest"))
	sig, err := SignDigest(digest, kp.PrivateKey())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !VerifyDigest(kp.PublicKeyCompressed(), digest, sig) {
			b.Fatal("驗證失敗")
		}
	}
}

func BenchmarkMerkleRoot1000(b *testing.B) {
	hashes := make([]string, 1000)
	for i := range hashes {
		h := SHA256([]byte{byte(i), byte(i >> 8), byte(i >> 16)})
		hashes[i] = hex.EncodeToString(h)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := MerkleRootStrings(hashes); err != nil {
			b.Fatal(err)
		}
	}
}
