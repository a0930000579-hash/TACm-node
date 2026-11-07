package crypto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

// EIP-712 類型化資料簽名引擎（eth_signTypedData_v4 相容）。
// 參考：https://eips.ethereum.org/EIPS/eip-712
//
// 對外入口：
//   - HashTypedDataV4(json) → 最終簽名哈希（keccak256("\x19\x01"‖domainSeparator‖structHash)）
//   - 內部拆成 EncodeType/TypeHash/DomainSeparator/StructHash 供單獨驗證。

// EIP712Field 一個 struct 欄位定義。
type EIP712Field struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// EIP712TypedData 完整 typed data 物件（v4 結構）。
type EIP712TypedData struct {
	Types       map[string][]EIP712Field `json:"types"`
	PrimaryType string                   `json:"primaryType"`
	Domain      map[string]any           `json:"domain"`
	Message     map[string]any           `json:"message"`
}

// HashTypedDataV4 解析 JSON 後計算 EIP-712 v4 最終簽名哈希。
func HashTypedDataV4(jsonData []byte) ([]byte, error) {
	var td EIP712TypedData
	dec := json.NewDecoder(bytes.NewReader(jsonData))
	dec.UseNumber()
	if err := dec.Decode(&td); err != nil {
		return nil, fmt.Errorf("eip712: JSON 解析失敗: %w", err)
	}
	return td.Hash()
}

// Hash 計算最終簽名哈希：keccak256("\x19\x01" ‖ domainSeparator ‖ structHash)。
func (td *EIP712TypedData) Hash() ([]byte, error) {
	if err := td.validate(); err != nil {
		return nil, err
	}
	ds, err := td.DomainSeparator()
	if err != nil {
		return nil, err
	}
	sh, err := td.StructHash()
	if err != nil {
		return nil, err
	}
	return Keccak256([]byte{0x19, 0x01}, ds, sh), nil
}

func (td *EIP712TypedData) validate() error {
	if td.PrimaryType == "" {
		return errors.New("eip712: primaryType 為空")
	}
	if td.Types == nil || len(td.Types) == 0 {
		return errors.New("eip712: types 為空")
	}
	if _, ok := td.Types[td.PrimaryType]; !ok {
		return fmt.Errorf("eip712: primaryType %q 不在 types 中", td.PrimaryType)
	}
	return nil
}

// DomainSeparator 計算 domain separator：keccak256(encodeType(EIP712Domain)‖encodeData(domain))。
func (td *EIP712TypedData) DomainSeparator() ([]byte, error) {
	fields, ok := td.Types["EIP712Domain"]
	if !ok {
		// 空 domain：encodeData 為空（回 typeHash 本身）。
		typeHash, err := td.typeHash("EIP712Domain", []EIP712Field{})
		if err != nil {
			return nil, err
		}
		return typeHash, nil
	}
	return td.hashStruct("EIP712Domain", fields, td.Domain)
}

// StructHash 計算 primaryType 的 struct hash。
func (td *EIP712TypedData) StructHash() ([]byte, error) {
	fields := td.Types[td.PrimaryType]
	return td.hashStruct(td.PrimaryType, fields, td.Message)
}

// hashStruct 對 data 編碼後與 typeHash 拼接哈希（struct 的 encodeData 起點）。
func (td *EIP712TypedData) hashStruct(typeName string, fields []EIP712Field, data map[string]any) ([]byte, error) {
	typeHash, err := td.typeHash(typeName, fields)
	if err != nil {
		return nil, err
	}
	enc, err := td.encodeData(typeName, fields, data)
	if err != nil {
		return nil, err
	}
	parts := [][]byte{typeHash}
	parts = append(parts, enc...)
	return Keccak256(parts...), nil
}

// typeHash 計算 keccak256(encodeType(typeName))。
func (td *EIP712TypedData) typeHash(typeName string, fields []EIP712Field) ([]byte, error) {
	enc, err := td.encodeType(typeName, fields)
	if err != nil {
		return nil, err
	}
	return Keccak256([]byte(enc)), nil
}

// encodeType 產生型別字串：primaryType 置首、其餘依字母序、遞迴收集依賴型別。
func (td *EIP712TypedData) encodeType(typeName string, fields []EIP712Field) (string, error) {
	seen := map[string]bool{}
	var deps []string
	var collect func(name string) error
	collect = func(name string) error {
		if seen[name] {
			return nil
		}
		seen[name] = true
		fs, ok := td.Types[name]
		if !ok {
			return fmt.Errorf("eip712: 型別 %q 未定義", name)
		}
		for _, f := range fs {
			base := f.Type
			if strings.HasSuffix(base, "[]") {
				base = base[:len(base)-2]
			} else if strings.HasPrefix(base, "bytes") {
				// bytesN（bytes1..bytes32）為原子型別，不遞迴。
				if isAtomicBytes(base) {
					continue
				}
			}
			if isReferenceType(base) {
				if err := collect(base); err != nil {
					return err
				}
			}
		}
		deps = append(deps, name)
		return nil
	}
	// primaryType 置首，其餘排序。
	if err := collect(typeName); err != nil {
		return "", err
	}
	rest := make([]string, 0, len(deps))
	for _, d := range deps {
		if d != typeName {
			rest = append(rest, d)
		}
	}
	sort.Strings(rest)
	order := append([]string{typeName}, rest...)
	var sb strings.Builder
	for _, name := range order {
		fs := td.Types[name]
		sb.WriteString(name)
		sb.WriteString("(")
		for i, f := range fs {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(f.Type)
			sb.WriteString(" ")
			sb.WriteString(f.Name)
		}
		sb.WriteString(")")
	}
	return sb.String(), nil
}

// isAtomicBytes 判斷 bytesN（1..32）原子型別。
func isAtomicBytes(t string) bool {
	if !strings.HasPrefix(t, "bytes") {
		return false
	}
	n := strings.TrimPrefix(t, "bytes")
	if n == "" {
		return false // 動態 bytes → 參考型別（內容哈希）
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return false
		}
	}
	v, err := atoi10(n)
	if err != nil || v < 1 || v > 32 {
		return false
	}
	return true
}

// isReferenceType 判斷是否為 struct 參考型別（非基本型別）。
func isReferenceType(t string) bool {
	if strings.HasSuffix(t, "[]") {
		return true
	}
	if isAtomicBytes(t) {
		return false
	}
	switch t {
	case "address", "bool", "string", "bytes":
		return false
	}
	if strings.HasPrefix(t, "uint") || strings.HasPrefix(t, "int") {
		return false
	}
	if strings.HasPrefix(t, "bytes") {
		return false // 動態 bytes 視為原子（內容哈希）
	}
	return true
}

// encodeData 依欄位順序編碼 data 為 32-byte 片段陣列。
func (td *EIP712TypedData) encodeData(typeName string, fields []EIP712Field, data map[string]any) ([][]byte, error) {
	out := make([][]byte, 0, len(fields))
	for _, f := range fields {
		val, ok := data[f.Name]
		if !ok {
			return nil, fmt.Errorf("eip712: %s.%s 缺少值", typeName, f.Name)
		}
		enc, err := td.encodeField(f.Type, val)
		if err != nil {
			return nil, fmt.Errorf("eip712: %s.%s 編碼失敗: %w", typeName, f.Name, err)
		}
		out = append(out, enc)
	}
	return out, nil
}

// encodeField 將單一值編碼為 32-byte。
func (td *EIP712TypedData) encodeField(fieldType string, val any) ([]byte, error) {
	// 陣列型別：T[] 或 T[N]。
	if strings.HasSuffix(fieldType, "[]") {
		items, err := toSlice(val)
		if err != nil {
			return nil, err
		}
		base := fieldType[:len(fieldType)-2]
		return td.encodeArray(base, items)
	}
	if idx := strings.IndexByte(fieldType, '['); idx > 0 {
		base := fieldType[:idx]
		items, err := toSlice(val)
		if err != nil {
			return nil, err
		}
		return td.encodeArray(base, items)
	}
	switch {
	case fieldType == "address":
		return encodeAddress(val)
	case fieldType == "bool":
		return encodeBool(val)
	case fieldType == "string":
		return encodeBytesLike(val)
	case fieldType == "bytes":
		return encodeBytesLike(val)
	case isAtomicBytes(fieldType):
		return encodeFixedBytes(fieldType, val)
	case strings.HasPrefix(fieldType, "uint"):
		return encodeUint(val)
	case strings.HasPrefix(fieldType, "int"):
		return encodeInt(val)
	default:
		// struct 參考型別：keccak256(encodeType ‖ encodeData)。
		fields, ok := td.Types[fieldType]
		if !ok {
			return nil, fmt.Errorf("eip712: 未知型別 %q", fieldType)
		}
		sub, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("eip712: %q 需要物件值", fieldType)
		}
		th, err := td.typeHash(fieldType, fields)
		if err != nil {
			return nil, err
		}
		enc, err := td.encodeData(fieldType, fields, sub)
		if err != nil {
			return nil, err
		}
		return Keccak256(append([][]byte{th}, enc...)...), nil
	}
}

// encodeArray 編碼陣列：keccak256(concat(每元素 32-byte 編碼))。
func (td *EIP712TypedData) encodeArray(base string, items []any) ([]byte, error) {
	parts := make([][]byte, 0, len(items))
	for _, it := range items {
		enc, err := td.encodeField(base, it)
		if err != nil {
			return nil, err
		}
		parts = append(parts, enc)
	}
	return Keccak256(parts...), nil
}

// encodeAddress 將地址字串（0x…/裸 hex）編碼為右對齊 32-byte。
func encodeAddress(val any) ([]byte, error) {
	s, ok := val.(string)
	if !ok {
		return nil, errors.New("address 需要字串")
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) != 40 {
		return nil, fmt.Errorf("address 長度需 40 hex，當前 %d", len(s))
	}
	b, err := hexDecode(s)
	if err != nil {
		return nil, err
	}
	return rightPad32(b), nil
}

// encodeBool 將布林編碼為 32-byte（0/1）。
func encodeBool(val any) ([]byte, error) {
	b, ok := val.(bool)
	if !ok {
		return nil, errors.New("bool 需要布林值")
	}
	out := make([]byte, 32)
	if b {
		out[31] = 1
	}
	return out, nil
}

// encodeBytesLike 將 string/bytes 做 keccak256（32-byte 結果）。
// 0x 前綴視為 hex bytes；否則按原始 UTF-8 字串處理。
func encodeBytesLike(val any) ([]byte, error) {
	s, ok := val.(string)
	if !ok {
		return nil, errors.New("string/bytes 需要字串")
	}
	raw := []byte(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		b, err := hexDecode(s[2:])
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return Keccak256(raw), nil
}

// encodeFixedBytes 將 bytesN（1..32）右對齊填零為 32-byte（不哈希）。
func encodeFixedBytes(t string, val any) ([]byte, error) {
	s, ok := val.(string)
	if !ok {
		return nil, errors.New("bytesN 需要字串")
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	b, err := hexDecode(s)
	if err != nil {
		return nil, err
	}
	n, _ := atoi10(strings.TrimPrefix(t, "bytes"))
	if n < 1 || n > 32 {
		return nil, fmt.Errorf("bytesN 需 1..32，當前 %d", n)
	}
	if len(b) > n {
		return nil, fmt.Errorf("bytes%d 內容 %d 字節超長", n, len(b))
	}
	return rightPad32(b), nil
}

// encodeUint 將 uintN 值（json.Number/string）右對齊 32-byte。
func encodeUint(val any) ([]byte, error) {
	bi, err := toBigInt(val)
	if err != nil || bi.Sign() < 0 {
		return nil, errors.New("uint 需要非負整數")
	}
	return bigIntTo32(bi), nil
}

// encodeInt 將 intN 值（json.Number/string）以大端補碼 32-byte 編碼。
func encodeInt(val any) ([]byte, error) {
	bi, err := toBigInt(val)
	if err != nil {
		return nil, err
	}
	return bigIntTo32(bi), nil
}

// toBigInt 將 json.Number / string / float 轉 big.Int。
func toBigInt(val any) (*big.Int, error) {
	switch v := val.(type) {
	case json.Number:
		bi, ok := new(big.Int).SetString(v.String(), 10)
		if !ok {
			return nil, fmt.Errorf("無法解析整數 %q", v.String())
		}
		return bi, nil
	case string:
		s := v
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			bi, ok := new(big.Int).SetString(s[2:], 16)
			if !ok {
				return nil, fmt.Errorf("無法解析 hex 整數 %q", s)
			}
			return bi, nil
		}
		bi, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("無法解析整數 %q", s)
		}
		return bi, nil
	case float64:
		return big.NewInt(int64(v)), nil
	default:
		return nil, fmt.Errorf("整數欄位需要數字或字串，得到 %T", val)
	}
}

// toSlice 將 JSON 陣列轉 []any。
func toSlice(val any) ([]any, error) {
	switch v := val.(type) {
	case []any:
		return v, nil
	default:
		return nil, fmt.Errorf("陣列欄位需要陣列值，得到 %T", val)
	}
}

// rightPad32 右對齊填零為 32-byte。
func rightPad32(b []byte) []byte {
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// bigIntTo32 將 big.Int 右對齊 32-byte（補碼/無號均以大端位元組）。
func bigIntTo32(bi *big.Int) []byte {
	b := bi.Bytes()
	if len(b) > 32 {
		b = b[len(b)-32:]
	}
	return rightPad32(b)
}

// atoi10 解析十進位非負整數字串。
func atoi10(s string) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("非數字字元 %q", string(c))
		}
		v = v*10 + int(c-'0')
	}
	return v, nil
}
