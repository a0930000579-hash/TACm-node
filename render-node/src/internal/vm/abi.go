package vm

// M139：Ethereum ABI 參數編碼/解碼（支援 Explorer/合約驗證中心的 read 互動）。
// 支持型別：uint8..uint256、int8..int256、address、bool、string、bytes、
//           bytesN(1..32)、靜態陣列 T[n]、動態陣列 T[]。
// 規則遵循 Solidity ABI v2 spec：靜態型 head=值；動態型 head=tail offset。

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"tacm/internal/crypto"
)

// abiTypeRe 解析型別：uint256、int64、address、bool、string、bytes、bytes32、
// uint8[]、address[3]、string[][2] 等。
var abiTypeRe = regexp.MustCompile(`^(u?int)(\d+)?|^(address|bool|string|bytes(\d+)?)((\[[0-9]*\])*)$`)

// parseABIType 把型別字串切成「基本型 + 陣列維度」。
// 回 base（uint/int/address/bool/string/bytes/bytesN）與維度列表（nil 代表非陣列）。
func parseABIType(t string) (base string, dims []int, err error) {
	t = strings.TrimSpace(t)
	// 陣列維度（由外向內解析，例如 string[][2] → [2][0]？依 ABI，巢狀陣列由內向外）。
	// 實作採「由左往右」掃描：每組 [n] 或 [] 由右側剝離，由內向外。
	parts := []int{}
	for {
		if strings.HasSuffix(t, "[]") {
			parts = append([]int{-1}, parts...)
			t = strings.TrimSuffix(t, "[]")
			continue
		}
		idx := strings.LastIndex(t, "[")
		if idx > 0 && strings.HasSuffix(t, "]") {
			inner := t[idx+1 : len(t)-1]
			if inner == "" {
				// 動態維度（理論上僅最外層允許，寬鬆接受）
				parts = append([]int{-1}, parts...)
			} else {
				n, perr := strconv.Atoi(inner)
				if perr != nil || n <= 0 {
					return "", nil, fmt.Errorf("abi: 陣列維度無效 %q", t)
				}
				parts = append([]int{n}, parts...)
			}
			t = t[:idx]
			continue
		}
		break
	}
	base = t
	if base == "" {
		return "", nil, errors.New("abi: 空型別")
	}
	switch {
	case base == "address" || base == "bool" || base == "string" || base == "bytes":
		return base, parts, nil
	case strings.HasPrefix(base, "uint"):
		bits := strings.TrimPrefix(base, "uint")
		if bits == "" {
			bits = "256"
		}
		n, perr := strconv.Atoi(bits)
		if perr != nil || n < 8 || n > 256 || n%8 != 0 {
			return "", nil, fmt.Errorf("abi: uint 位寬無效 %q", base)
		}
		return "uint", parts, nil
	case strings.HasPrefix(base, "int"):
		bits := strings.TrimPrefix(base, "int")
		if bits == "" {
			bits = "256"
		}
		n, perr := strconv.Atoi(bits)
		if perr != nil || n < 8 || n > 256 || n%8 != 0 {
			return "", nil, fmt.Errorf("abi: int 位寬無效 %q", base)
		}
		return "int", parts, nil
	case strings.HasPrefix(base, "bytes"):
		rest := strings.TrimPrefix(base, "bytes")
		if rest == "" {
			return "bytes", parts, nil // 動態 bytes
		}
		n, perr := strconv.Atoi(rest)
		if perr != nil || n < 1 || n > 32 {
			return "", nil, fmt.Errorf("abi: bytesN 長度無效 %q", base)
		}
		return "bytesN", parts, nil
	}
	return "", nil, fmt.Errorf("abi: 未知型別 %q", t)
}

// isDynamicABI 判斷型別是否為動態（需 tail 存值）。
func isDynamicABI(t string) (bool, error) {
	base, dims, err := parseABIType(t)
	if err != nil {
		return false, err
	}
	if base == "string" || base == "bytes" {
		return true, nil
	}
	if len(dims) > 0 {
		// 任何維度為動態 → 動態
		for _, d := range dims {
			if d == -1 {
				return true, nil
			}
		}
		// 全靜態維度但元素為動態 → 動態
		inner := ""
		for i := len(dims) - 1; i >= 0; i-- {
			if dims[i] == -1 {
				inner = "[]" + inner
			} else {
				inner = fmt.Sprintf("[%d]", dims[i]) + inner
			}
		}
		inner = base + inner
		// 元素型別（剝最內層維度）
		ib, id, ierr := parseABIType(inner)
		if ierr != nil {
			return false, ierr
		}
		if ib == "string" || ib == "bytes" {
			return true, nil
		}
		for _, d := range id {
			if d == -1 {
				return true, nil
			}
		}
		return false, nil
	}
	return false, nil
}

// toUint 把 JSON 值轉 big.Int（接受 number、十進位字串、0x hex）。
func toUint(v any) (*big.Int, error) {
	switch x := v.(type) {
	case float64:
		if x != float64(int64(x)) {
			return nil, errors.New("abi: 數字非整數")
		}
		return big.NewInt(int64(x)), nil
	case int:
		return big.NewInt(int64(x)), nil
	case int64:
		return big.NewInt(x), nil
	case json.Number:
		if strings.HasPrefix(x.String(), "0x") || strings.HasPrefix(x.String(), "0X") {
			b, ok := new(big.Int).SetString(strings.TrimPrefix(x.String(), "0x"), 16)
			if !ok {
				return nil, fmt.Errorf("abi: hex 數字無效 %q", x.String())
			}
			return b, nil
		}
		b, ok := new(big.Int).SetString(x.String(), 10)
		if !ok {
			return nil, fmt.Errorf("abi: 數字無效 %q", x.String())
		}
		return b, nil
	case string:
		if strings.HasPrefix(x, "0x") || strings.HasPrefix(x, "0X") {
			b, ok := new(big.Int).SetString(strings.TrimPrefix(x, "0x"), 16)
			if !ok {
				return nil, fmt.Errorf("abi: hex 數字無效 %q", x)
			}
			return b, nil
		}
		b, ok := new(big.Int).SetString(x, 10)
		if !ok {
			return nil, fmt.Errorf("abi: 數字無效 %q", x)
		}
		return b, nil
	case *big.Int:
		return x, nil
	}
	return nil, fmt.Errorf("abi: 無法解析數字 %T", v)
}

// encodeStatic 把單一靜態值編成 32-byte（uint/int/address/bool/bytesN 右對齊）。
func encodeStatic(t string, v any) ([]byte, error) {
	base, _, err := parseABIType(t)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 32)
	switch base {
	case "uint", "int":
		b, uerr := toUint(v)
		if uerr != nil {
			return nil, uerr
		}
		if b.Sign() < 0 && base == "uint" {
			return nil, errors.New("abi: uint 不接受負數")
		}
		// 位寬截斷檢查（256 以內，超出高位直接忽略——保持寬鬆；主流工具亦如此）
		bytes := b.Bytes()
		if len(bytes) > 32 {
			return nil, errors.New("abi: 數字超出 256 位")
		}
		copy(out[32-len(bytes):], bytes)
		if base == "int" && b.Sign() < 0 {
			// 二補數：2^256 + b
			twos := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 256), b)
			tb := twos.Bytes()
			if len(tb) > 32 {
				tb = tb[len(tb)-32:]
			}
			out = make([]byte, 32)
			copy(out[32-len(tb):], tb)
		}
		return out, nil
	case "address":
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("abi: address 需為字串")
		}
		s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
		if len(s) != 40 {
			return nil, fmt.Errorf("abi: address 長度無效 %q", s)
		}
		b, herr := hex.DecodeString(s)
		if herr != nil {
			return nil, fmt.Errorf("abi: address hex 無效 %q", s)
		}
		copy(out[12:], b)
		return out, nil
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("abi: bool 需為布林")
		}
		if b {
			out[31] = 1
		}
		return out, nil
	case "bytesN":
		rest := strings.TrimPrefix(t, "bytes")
		n, _ := strconv.Atoi(rest)
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("abi: bytesN 需為 hex 字串")
		}
		b, herr := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"))
		if herr != nil {
			return nil, fmt.Errorf("abi: bytesN hex 無效 %q", s)
		}
		if len(b) > n {
			return nil, fmt.Errorf("abi: bytes%d 至多 %d 字節，得 %d", n, n, len(b))
		}
		copy(out[:len(b)], b) // ABI spec：bytesN 內容靠 word 左側（高位），低位補零
		return out, nil
	}
	return nil, fmt.Errorf("abi: %s 非靜態標量", t)
}

// EncodeArgs 依 ABI types 編碼參數為 calldata（不含 selector）。
// args 須與 types 一一對應；動態值依 ABI spec 排入 tail，head 存相對編碼起點的 offset。
func EncodeArgs(types []string, args []any) ([]byte, error) {
	if len(types) != len(args) {
		return nil, fmt.Errorf("abi: 參數數量 %d 與型別 %d 不符", len(args), len(types))
	}
	// 第一遍：計算最終 head 區塊大小（動態參數 head 佔 32；靜態參數佔其靜態大小）。
	totalHead := 0
	for _, t := range types {
		dyn, err := isDynamicABI(t)
		if err != nil {
			return nil, err
		}
		if dyn {
			totalHead += 32
		} else {
			totalHead += abiStaticSize(t)
		}
	}
	// 第二遍：組 head/tail。
	head := []byte{}
	tail := []byte{}
	for i, t := range types {
		dyn, err := isDynamicABI(t)
		if err != nil {
			return nil, err
		}
		if !dyn {
			enc, eerr := encodeValue(t, args[i])
			if eerr != nil {
				return nil, eerr
			}
			head = append(head, enc...)
			continue
		}
		// 動態：head 存 tail offset（相對整個 encoding 起點＝totalHead＋已排 tail）
		offset := totalHead + len(tail)
		head = append(head, bigIntToWord32(big.NewInt(int64(offset)))...)
		enc, eerr := encodeValue(t, args[i])
		if eerr != nil {
			return nil, eerr
		}
		tail = append(tail, enc...)
	}
	return append(head, tail...), nil
}

// bigIntToWord32 回 32-byte 大端表示。
func bigIntToWord32(b *big.Int) []byte {
	out := make([]byte, 32)
	bb := b.Bytes()
	if len(bb) > 32 {
		bb = bb[len(bb)-32:]
	}
	copy(out[32-len(bb):], bb)
	return out
}

// encodeValue 依型別編碼單值（含動態型與陣列）。
func encodeValue(t string, v any) ([]byte, error) {
	base, dims, err := parseABIType(t)
	if err != nil {
		return nil, err
	}
	if len(dims) == 0 {
		switch base {
		case "string":
			s, ok := v.(string)
			if !ok {
				return nil, errors.New("abi: string 需為字串")
			}
			b := []byte(s)
			out := append(bigIntToWord32(big.NewInt(int64(len(b)))), padRight(b)...)
			return out, nil
		case "bytes":
			s, ok := v.(string)
			if !ok {
				return nil, errors.New("abi: bytes 需為 hex 字串")
			}
			b, herr := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"))
			if herr != nil {
				return nil, fmt.Errorf("abi: bytes hex 無效 %q", s)
			}
			out := append(bigIntToWord32(big.NewInt(int64(len(b)))), padRight(b)...)
			return out, nil
		}
		return encodeStatic(t, v)
	}
	// 陣列：最內層維度為元素集合。
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("abi: %s 需為陣列", t)
	}
	innerDims := dims[1:]
	innerT := renderABIType(base, innerDims)
	elemDynamic, eerr := isDynamicABI(innerT)
	if eerr != nil {
		return nil, eerr
	}
	// 編碼元素
	head := []byte{}
	tail := []byte{}
	for _, e := range arr {
		enc, eerr := encodeValue(innerT, e)
		if eerr != nil {
			return nil, eerr
		}
		if !elemDynamic {
			head = append(head, enc...)
		} else {
			offset := len(head) + len(tail)
			head = append(head, bigIntToWord32(big.NewInt(int64(offset)))...)
			tail = append(tail, enc...)
		}
	}
	body := append(head, tail...)
	if dims[0] == -1 {
		// 動態陣列：length + body
		return append(bigIntToWord32(big.NewInt(int64(len(arr)))), body...), nil
	}
	// 靜態陣列：直接 body
	return body, nil
}

// renderABIType 依 base+dims 組回型別字串（例如 base=string dims=[2] → string[2]）。
func renderABIType(base string, dims []int) string {
	out := base
	for _, d := range dims {
		if d == -1 {
			out += "[]"
		} else {
			out += fmt.Sprintf("[%d]", d)
		}
	}
	return out
}

// padRight 補到 32 倍數。
func padRight(b []byte) []byte {
	pad := (32 - len(b)%32) % 32
	return append(b, make([]byte, pad)...)
}

// FuncSelector 回函式選擇器（keccak256(sig)[:4]）。
func FuncSelector(sig string) ([]byte, error) {
	if strings.TrimSpace(sig) == "" {
		return nil, errors.New("abi: 空函式簽名")
	}
	h := crypto.Keccak256([]byte(sig))
	return h[:4], nil
}

// DecodeOutputs 依 ABI output 型別解碼 return data 為可讀值。
// 回值規則：uint/int→十進位字串、address→0x+40hex、bool→布林、
//          string→utf8、bytes/bytesN→0xhex、陣列→[]any。
func DecodeOutputs(types []string, data []byte) ([]any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	out := make([]any, 0, len(types))
	pos := 0
	for _, t := range types {
		dyn, err := isDynamicABI(t)
		if err != nil {
			return nil, err
		}
		if !dyn {
			need := abiStaticSize(t)
			if pos+need > len(data) {
				return nil, fmt.Errorf("abi: return data 不足（型別 %s 需 %d 字節）", t, need)
			}
			val, err := decodeOutputAt(t, data[pos:pos+need])
			if err != nil {
				return nil, err
			}
			out = append(out, val)
			pos += need
			continue
		}
		// 動態：head word（相對 return data 起點）
		if pos+32 > len(data) {
			return nil, errors.New("abi: return data 不足（動態 head）")
		}
		rel := int(new(big.Int).SetBytes(data[pos : pos+32]).Int64())
		pos += 32
		if rel < 0 || rel > len(data) {
			return nil, errors.New("abi: head offset 越界")
		}
		val, err := decodeOutputAt(t, data[rel:])
		if err != nil {
			return nil, err
		}
		out = append(out, val)
	}
	return out, nil
}

// abiStaticSize 回靜態型在 encoding 中的佔用（陣列為元素數×元素大小）。
func abiStaticSize(t string) int {
	base, dims, err := parseABIType(t)
	if err != nil {
		return 0
	}
	if len(dims) == 0 {
		return 32
	}
	n := 1
	for _, d := range dims {
		if d == -1 {
			return 0
		}
		n *= d
	}
	inner := base
	for i := 1; i < len(dims); i++ {
		if dims[i] == -1 {
			inner += "[]"
		} else {
			inner += fmt.Sprintf("[%d]", dims[i])
		}
	}
	return n * abiStaticSize(inner)
}

// decodeOutputAt 依型別解碼「值起點」的資料：
// - 靜態標量：data 開頭即值。
// - 動態標量（string/bytes）：data 開頭即 length。
// - 動態陣列：data 開頭即 length，元素 head 相對 data 起點。
// - 靜態陣列：data 開頭即元素序列。
func decodeOutputAt(t string, data []byte) (any, error) {
	base, dims, err := parseABIType(t)
	if err != nil {
		return nil, err
	}
	if len(dims) == 0 {
		switch base {
		case "uint", "int":
			if len(data) < 32 {
				return nil, errors.New("abi: 解碼 uint 資料不足")
			}
			w := new(big.Int).SetBytes(data[:32])
			if base == "int" && data[0]&0x80 != 0 {
				neg := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), w)
				w = neg
			}
			return w.String(), nil
		case "address":
			if len(data) < 32 {
				return nil, errors.New("abi: 解碼 address 資料不足")
			}
			return "0x" + hex.EncodeToString(data[12:32]), nil
		case "bool":
			if len(data) < 32 {
				return nil, errors.New("abi: 解碼 bool 資料不足")
			}
			return data[31] != 0, nil
		case "string":
			if len(data) < 32 {
				return nil, errors.New("abi: 解碼 string 資料不足")
			}
			l := int(new(big.Int).SetBytes(data[:32]).Int64())
			if l < 0 || 32+l > len(data) {
				return nil, errors.New("abi: string 資料不足")
			}
			return string(data[32 : 32+l]), nil
		case "bytes":
			if len(data) < 32 {
				return nil, errors.New("abi: 解碼 bytes 資料不足")
			}
			l := int(new(big.Int).SetBytes(data[:32]).Int64())
			if l < 0 || 32+l > len(data) {
				return nil, errors.New("abi: bytes 資料不足")
			}
			return "0x" + hex.EncodeToString(data[32 : 32+l]), nil
		case "bytesN":
			rest := strings.TrimPrefix(t, "bytes")
			n, _ := strconv.Atoi(rest)
			if len(data) < 32 {
				return nil, errors.New("abi: 解碼 bytesN 資料不足")
			}
			return "0x" + hex.EncodeToString(data[:n]), nil
		}
		return nil, fmt.Errorf("abi: 未知型別 %s", t)
	}
	// 陣列
	elemT := renderABIType(base, dims[1:])
	elemDyn, _ := isDynamicABI(elemT)
	if dims[0] == -1 {
		// 動態陣列：data 開頭即 length
		if len(data) < 32 {
			return nil, errors.New("abi: 陣列 length 不足")
		}
		l := int(new(big.Int).SetBytes(data[:32]).Int64())
		if l < 0 || 32 > len(data) {
			return nil, errors.New("abi: 陣列長度異常")
		}
		cur := 32
		elems := []any{}
		for i := 0; i < l; i++ {
			if elemDyn {
				if cur+32 > len(data) {
					return nil, errors.New("abi: 陣列元素 head 越界")
				}
				rel := int(new(big.Int).SetBytes(data[cur : cur+32]).Int64())
				cur += 32
				if rel < 0 || rel > len(data) {
					return nil, errors.New("abi: 陣列元素 offset 越界")
				}
				val, err := decodeOutputAt(elemT, data[rel:])
				if err != nil {
					return nil, err
				}
				elems = append(elems, val)
			} else {
				size := abiStaticSize(elemT)
				if cur+size > len(data) {
					return nil, errors.New("abi: 陣列元素資料不足")
				}
				val, err := decodeOutputAt(elemT, data[cur:cur+size])
				if err != nil {
					return nil, err
				}
				elems = append(elems, val)
				cur += size
			}
		}
		return elems, nil
	}
	// 靜態陣列：data 開頭即元素序列
	n := dims[0]
	cur := 0
	elems := []any{}
	for i := 0; i < n; i++ {
		if elemDyn {
			if cur+32 > len(data) {
				return nil, errors.New("abi: 靜態陣列元素 head 越界")
			}
			rel := int(new(big.Int).SetBytes(data[cur : cur+32]).Int64())
			cur += 32
			if rel < 0 || rel > len(data) {
				return nil, errors.New("abi: 靜態陣列元素 offset 越界")
			}
			val, err := decodeOutputAt(elemT, data[rel:])
			if err != nil {
				return nil, err
			}
			elems = append(elems, val)
		} else {
			size := abiStaticSize(elemT)
			if cur+size > len(data) {
				return nil, errors.New("abi: 靜態陣列元素資料不足")
			}
			val, err := decodeOutputAt(elemT, data[cur:cur+size])
			if err != nil {
				return nil, err
			}
			elems = append(elems, val)
			cur += size
		}
	}
	return elems, nil
}
