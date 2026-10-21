package crypto

import (
	"fmt"
)

// merkleEmptyRoot 是空交易列表的佔位根：double_sha256(b"EMPTY")。
func merkleEmptyRoot() string {
	return fmt.Sprintf("%x", DoubleSHA256([]byte("EMPTY")))
}

// toHashBytes 把葉子項（[]byte 或 string）做一次 SHA-256；其他類型返回 nil。
func toHashBytes(item any) []byte {
	switch t := item.(type) {
	case []byte:
		return SHA256(t)
	case string:
		return SHA256([]byte(t))
	default:
		return nil
	}
}

// MerkleRoot 計算 SHA-256 Merkle 根（hex）。
// 空列表返回 double_sha256("EMPTY")；奇數層末葉自我複製；葉子可為 string 或 []byte。
func MerkleRoot(items []any) (string, error) {
	if len(items) == 0 {
		return merkleEmptyRoot(), nil
	}
	level := make([][]byte, len(items))
	for i, it := range items {
		level[i] = toHashBytes(it)
	}
	for len(level) > 1 {
		level = merkleNextLevel(level)
	}
	return fmt.Sprintf("%x", level[0]), nil
}

// MerkleRootStrings 是 MerkleRoot 的字符串便捷版本。
func MerkleRootStrings(txHashes []string) (string, error) {
	items := make([]any, len(txHashes))
	for i, h := range txHashes {
		items[i] = h
	}
	return MerkleRoot(items)
}

// merkleNextLevel 合併相鄰兩葉，奇數時複製末葉。
func merkleNextLevel(level [][]byte) [][]byte {
	nxt := make([][]byte, 0, (len(level)+1)/2)
	for i := 0; i < len(level); i += 2 {
		a := level[i]
		b := a
		if i+1 < len(level) {
			b = level[i+1]
		}
		nxt = append(nxt, SHA256(append(append([]byte{}, a...), b...)))
	}
	return nxt
}

// MerkleProof 返回從葉到根的兄弟節點 hex 列表（存在性證明）。index 越界返回空列表。
func MerkleProof(items []any, index int) ([]string, error) {
	if index < 0 || index >= len(items) {
		return []string{}, nil
	}
	level := make([][]byte, len(items))
	for i, it := range items {
		level[i] = toHashBytes(it)
	}
	proof := []string{}
	i := index
	for len(level) > 1 {
		nxt := make([][]byte, 0, (len(level)+1)/2)
		for p := 0; p < len(level); p += 2 {
			a := level[p]
			b := a
			if p+1 < len(level) {
				b = level[p+1]
			}
			if p == i || p+1 == i {
				sib := a
				if p == i {
					sib = b
				}
				proof = append(proof, fmt.Sprintf("%x", sib))
				i = len(nxt)
			}
			nxt = append(nxt, SHA256(append(append([]byte{}, a...), b...)))
		}
		level = nxt
	}
	return proof, nil
}
