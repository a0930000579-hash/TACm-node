// Package addrs 實現 TAC 業務平台側的規範地址（tx0 + base32(blake2s)）。
// 與 internal/crypto 的鏈上 Base58Check 地址共同構成「業務平台 + 自主鏈」雙層地址體系，
// 二者通過用戶身份（uid/email）關聯，不可只保留其一。
package addrs

import (
	"encoding/base32"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// DefaultHRP 為業務地址默認前綴。
	DefaultHRP = "tx0"
	v1Payload  = 20 // v1：base32 32 字符
	v2Payload  = 24 // v2：base32 39 字符（規範）
)

// hrp 為當前生效前綴，可由 SetHRP 同步配置。
var hrp = DefaultHRP

// SetHRP 設置地址前綴（小寫、去空白）。
func SetHRP(h string) { hrp = strings.ToLower(strings.TrimSpace(h)) }

// HRP 返回當前地址前綴。
func HRP() string { return hrp }

// b32encode 做小寫、無填充的 Base32 編碼。
func b32encode(b []byte) string {
	s := base32.StdEncoding.EncodeToString(b)
	return strings.ToLower(strings.TrimRight(s, "="))
}

// Tx0Address 從公鑰字節派生業務地址；version=2 為規範（39 字符），其餘為 v1（32 字符）。
func Tx0Address(pub []byte, version int) (string, error) {
	n := v2Payload
	if version != 2 {
		n = v1Payload
	}
	digest, err := SumBlake2s(pub, n)
	if err != nil {
		return "", err
	}
	return hrp + b32encode(digest), nil
}

// IsValidAddr 校驗業務地址：v2（39 字符）始終接受；v1（32 字符）僅在 acceptV1 時接受。
func IsValidAddr(s string, acceptV1 bool) bool {
	if s == "" {
		return false
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if addrRegex(39).MatchString(s) {
		return true
	}
	return acceptV1 && addrRegex(32).MatchString(s)
}

// addrRegex 返回「前綴 + n 個 base32 字符」的錨定正則。
func addrRegex(n int) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`^%s[a-z2-7]{%d}$`, regexp.QuoteMeta(hrp), n))
}

// LegacyToTx0 把任意舊字符串（按 UTF-8 字節）重新派生為規範 tx0 地址。
func LegacyToTx0(s string, version int) (string, error) {
	n := v2Payload
	if version != 2 {
		n = v1Payload
	}
	digest, err := SumBlake2s([]byte(s), n)
	if err != nil {
		return "", err
	}
	return hrp + b32encode(digest), nil
}

// ErrAddress 表示地址規範化失敗。
var ErrAddress = errors.New("addrs: 地址規範化失敗")

// NormalizeAddr 把 v1 / 舊前綴（tacm1、apc1）/ 任意字符串統一規範為 v2 tx0 地址。
func NormalizeAddr(s string) (string, error) {
	if s == "" {
		return s, nil
	}
	s = strings.ToLower(strings.TrimSpace(s))

	if IsValidAddr(s, false) { // 已是規範 v2
		return s, nil
	}
	if IsValidAddr(s, true) { // v1 → v2
		return LegacyToTx0(s, 2)
	}
	if strings.HasPrefix(s, "tacm1") || strings.HasPrefix(s, "apc1") {
		return LegacyToTx0(s, 2)
	}
	if m := regexp.MustCompile(`([a-z2-7]{32,52})$`).FindString(s); m != "" {
		return LegacyToTx0(m, 2)
	}
	out, err := LegacyToTx0(s, 2)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAddress, err)
	}
	return out, nil
}
