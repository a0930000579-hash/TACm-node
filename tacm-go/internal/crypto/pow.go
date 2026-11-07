package crypto

import (
	"bytes"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// PowResult 為一次成功 PoT 挖礦的結果（型別名保留歷史命名）。
type PowResult struct {
	Nonce int    `json:"nonce"`
	Hash  string `json:"hash"`
}

// MineBlock 尋找 nonce 使區塊哈希前 difficulty 個字符為 '0'。
// 在 maxNonce 內未找到返回 (nil, nil)；與 Python mine_block 等價。
//
// 效能：規範化表頭固定 8 個鍵、僅 nonce 逐次變化。舊法對每個 nonce 都重新做
// 整顆表頭的 Canonical JSON 序列化（難度 3 約數千次），為穩態 CPU 最大熱點之一。
// 此處先序列化 nonce 前/後的靜態片段，每輪只組裝 nonce 整數再雙重 SHA-256；
// 組裝位元組與逐次 Canonical 完全相同（由 TestMineBlockFastByteEquivalent 固化），
// 故區塊哈希與鏈上相容性不變。
func MineBlock(header map[string]any, difficulty, maxNonce int) (*PowResult, error) {
	target := strings.Repeat("0", difficulty)
	base := NormalizeBlockHeader(header)
	base["difficulty"] = difficulty
	prefix, suffix, err := canonicalNonceSplits(base)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for nonce := 0; nonce < maxNonce; nonce++ {
		buf.Reset()
		buf.Write(prefix)
		buf.WriteString(strconv.Itoa(nonce))
		buf.Write(suffix)
		h := doubleSHA256Hex(buf.Bytes())
		if strings.HasPrefix(h, target) {
			return &PowResult{Nonce: nonce, Hash: h}, nil
		}
	}
	return nil, nil
}

// canonicalNonceSplits 回傳規範化表頭在 nonce 值處切開的前綴（含 "nonce":）與後綴。
// 前綴＝'{' 到 nonce 鍵的冒號；後綴＝nonce 值之後的分隔/剩餘鍵值與 '}'。
// 與 writeCanonical 對整個 map 的輸出逐位元組一致（僅 nonce 整數留待逐輪填入）。
func canonicalNonceSplits(base map[string]any) (prefix, suffix []byte, err error) {
	keys := make([]string, 0, len(base))
	for k := range base {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ni := -1
	for i, k := range keys {
		if k == "nonce" {
			ni = i
			break
		}
	}
	if ni < 0 {
		return nil, nil, &CanonicalError{Value: base}
	}
	var pre, suf bytes.Buffer
	pre.WriteByte('{')
	for i := 0; i < ni; i++ {
		if i > 0 {
			pre.WriteByte(',')
		}
		writeJSONString(&pre, keys[i])
		pre.WriteByte(':')
		if err := writeCanonical(&pre, base[keys[i]]); err != nil {
			return nil, nil, err
		}
	}
	if ni > 0 {
		pre.WriteByte(',')
	}
	writeJSONString(&pre, "nonce")
	pre.WriteByte(':')
	if ni < len(keys)-1 {
		suf.WriteByte(',')
		for j := ni + 1; j < len(keys); j++ {
			if j > ni+1 {
				suf.WriteByte(',')
			}
			writeJSONString(&suf, keys[j])
			suf.WriteByte(':')
			if err := writeCanonical(&suf, base[keys[j]]); err != nil {
				return nil, nil, err
			}
		}
	}
	suf.WriteByte('}')
	return pre.Bytes(), suf.Bytes(), nil
}

// doubleSHA256Hex 對原始位元組做雙重 SHA-256 並回傳小寫 hex。
func doubleSHA256Hex(raw []byte) string {
	return hex.EncodeToString(DoubleSHA256(raw))
}

// VerifyPow 校驗區塊 PoT；內部固定 difficulty 字段，避免調用方遺漏（函式名保留歷史命名）。
func VerifyPow(header map[string]any, difficulty int) (bool, error) {
	test := cloneHeader(header)
	test["difficulty"] = difficulty
	h, err := HashBlockHeader(test)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(h, strings.Repeat("0", difficulty)), nil
}

// CalculateDifficulty 按高度簡化調整難度：每 100 塊 +1，上限 16，基準 4。
func CalculateDifficulty(currentHeight int) int {
	return calculateDifficulty(currentHeight, 4, 100)
}

func calculateDifficulty(currentHeight, baseDifficulty, adjustmentInterval int) int {
	if adjustmentInterval <= 0 {
		adjustmentInterval = 100
	}
	d := baseDifficulty + currentHeight/adjustmentInterval
	if d > 16 {
		return 16
	}
	return d
}

// cloneHeader 對區塊頭做淺拷貝，避免修改調用方的 map。
func cloneHeader(header map[string]any) map[string]any {
	dup := make(map[string]any, len(header))
	for k, v := range header {
		dup[k] = v
	}
	return dup
}
