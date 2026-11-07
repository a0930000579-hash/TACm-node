package crypto

import (
	"bytes"
	"strconv"
	"testing"
)

// TestMineBlockFastByteEquivalent 固化效能優化的安全前提：prefix + nonce + suffix
// 組裝出的位元組，必須與對整顆表頭逐次 Canonical 完全相同，連帶哈希相同，
// 確保優化不改变任何區塊哈希（鏈上相容性）。
func TestMineBlockFastByteEquivalent(t *testing.T) {
	header := map[string]any{
		"height":      344580,
		"prev_hash":   "00bb134bec0ceddf7be6cc839956d29ba6cc738965d1889289c7ccf8b10cd70a",
		"merkle_root": "ab12cd34",
		"proposer":    "tx01NcLkcmSQiGTQg8nE6pvw72jJHXPYmdShV",
		"ts":          1730900000,
		"tx_count":    3,
		"difficulty":  3,
		"nonce":       0,
	}
	base := NormalizeBlockHeader(header)
	base["difficulty"] = 3
	prefix, suffix, err := canonicalNonceSplits(base)
	if err != nil {
		t.Fatalf("splits 失敗: %v", err)
	}
	for nonce := 0; nonce < 5000; nonce++ {
		want := cloneHeader(base)
		want["nonce"] = nonce
		wantBytes, err := Canonical(want)
		if err != nil {
			t.Fatalf("參考 Canonical 失敗: %v", err)
		}
		got := bytes.Join([][]byte{prefix, []byte(strconv.Itoa(nonce)), suffix}, nil)
		if !bytes.Equal(got, wantBytes) {
			t.Fatalf("nonce=%d 位元組不一致\n got=%s\nwant=%s", nonce, got, wantBytes)
		}
		if doubleSHA256Hex(got) != doubleSHA256Hex(wantBytes) {
			t.Fatalf("nonce=%d 哈希不一致", nonce)
		}
	}
}

// TestCanonicalNonceSplitsPositions 驗證 nonce 位於首/中/末時切分仍逐位元組等價。
func TestCanonicalNonceSplitsPositions(t *testing.T) {
	cases := []map[string]any{
		{"nonce": 0, "z": 1},            // nonce 排序在前
		{"a": 1, "nonce": 0, "z": 2},    // 中間
		{"m": 1, "nonce": 0},            // nonce 排序在末
	}
	for _, m := range cases {
		prefix, suffix, err := canonicalNonceSplits(m)
		if err != nil {
			t.Fatalf("splits 失敗: %v", err)
		}
		for nonce := 0; nonce < 50; nonce++ {
			want := cloneHeader(m)
			want["nonce"] = nonce
			wantBytes, _ := Canonical(want)
			got := bytes.Join([][]byte{prefix, []byte(strconv.Itoa(nonce)), suffix}, nil)
			if !bytes.Equal(got, wantBytes) {
				t.Fatalf("m=%v nonce=%d 不一致", m, nonce)
			}
		}
	}
}

// TestMineBlockResultVerifies 端到端：MineBlock 找到的 nonce 必須通過 VerifyPow，
// 且其哈希與 HashBlockHeader（鏈上驗證路徑）獨立重算一致。
func TestMineBlockResultVerifies(t *testing.T) {
	header := map[string]any{
		"height": 12, "prev_hash": "abc", "merkle_root": "deff",
		"proposer": "tx01x", "ts": 1000, "tx_count": 1,
		"difficulty": 2, "nonce": 0,
	}
	res, err := MineBlock(header, 2, 200000)
	if err != nil {
		t.Fatalf("MineBlock 錯誤: %v", err)
	}
	if res == nil {
		t.Fatal("應找到 nonce")
	}
	chk := cloneHeader(header)
	chk["nonce"] = res.Nonce
	ok, err := VerifyPow(chk, 2)
	if err != nil || !ok {
		t.Fatalf("VerifyPow 應通過: ok=%v err=%v", ok, err)
	}
	ind, err := HashBlockHeader(chk)
	if err != nil {
		t.Fatalf("HashBlockHeader 錯誤: %v", err)
	}
	if ind != res.Hash {
		t.Fatalf("哈希 %s 應等於獨立重算 %s", res.Hash, ind)
	}
}
