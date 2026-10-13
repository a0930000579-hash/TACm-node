// Package crypto 實現 TAC Ai 智能鏈的密碼學原語：
// secp256k1 密鑰、tx0 Base58Check 地址、雙重 SHA-256、Hash160、Merkle、PoW 與交易簽名。
//
// 本包僅使用通用密碼學標準（secp256k1/SHA-256/RIPEMD-160/keccak/Base58），
// 不依賴任何外部公鏈的節點或客戶端，共識與賬本完全由 TAC 鏈自有。
package crypto

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"

	// 通用哈希標準（Go 官方擴展），非任何鏈的客戶端
	"golang.org/x/crypto/ripemd160"
)

// SHA256 返回單次 SHA-256 摘要。
func SHA256(data []byte) []byte {
	h := newSHA256()
	h.Write(data)
	return h.Sum(nil)
}

// DoubleSHA256 返回雙重 SHA-256 摘要（比特幣式區塊/校驗標準）。
func DoubleSHA256(data []byte) []byte {
	return SHA256(SHA256(data))
}

// RIPEMD160 返回 RIPEMD-160 摘要。
func RIPEMD160(data []byte) []byte {
	h := ripemd160.New()
	h.Write(data)
	return h.Sum(nil)
}

// Hash160 = RIPEMD-160(SHA-256(data))，用於公鑰→地址。
func Hash160(data []byte) []byte {
	return RIPEMD160(SHA256(data))
}

// Canonical 對結構做穩定序列化：鍵排序、緊湊分隔、不轉義 HTML/多字節字符，
// 與 Python 端 json.dumps(sort_keys=True, separators=(",",":"), ensure_ascii=False) 等價。
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, t[i]); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case string:
		writeJSONString(buf, t)
	case json.Number:
		buf.WriteString(t.String())
	case int:
		buf.WriteString(strconv.Itoa(t))
	case int64:
		buf.WriteString(strconv.FormatInt(t, 10))
	case uint64:
		buf.WriteString(strconv.FormatUint(t, 10))
	case float64:
		// 交易金額一律以字符串傳遞；此分支僅作兜底，使用最短往返表示。
		buf.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case nil:
		buf.WriteString("null")
	default:
		return &CanonicalError{Value: v}
	}
	return nil
}

// writeJSONString 以 JSON 字符串規則寫入，關閉 HTML 轉義（匹配 ensure_ascii=False）。
func writeJSONString(buf *bytes.Buffer, s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := b.Bytes()
	buf.Write(out[:len(out)-1]) // 去掉 Encode 追加的換行符
}

// CanonicalError 表示遇到無法穩定序列化的類型。
type CanonicalError struct {
	Value any
}

func (e *CanonicalError) Error() string {
	return "crypto: 無法對該類型做規範序列化: " + typeof(e.Value)
}
