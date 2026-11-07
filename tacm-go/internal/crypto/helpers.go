package crypto

import (
	"crypto/sha256"
	"fmt"
	"hash"
)

// newSHA256 返回標準庫 SHA-256 hash.Hash。
func newSHA256() hash.Hash { return sha256.New() }

// typeof 返回值的動態類型名（用於錯誤信息）。
func typeof(v any) string { return fmt.Sprintf("%T", v) }
