package crypto

import (
	"strings"
)

// PowResult 為一次成功 PoW 的結果。
type PowResult struct {
	Nonce int    `json:"nonce"`
	Hash  string `json:"hash"`
}

// MineBlock 尋找 nonce 使區塊哈希前 difficulty 個字符為 '0'。
// 在 maxNonce 內未找到返回 (nil, nil)；與 Python mine_block 等價。
func MineBlock(header map[string]any, difficulty, maxNonce int) (*PowResult, error) {
	target := strings.Repeat("0", difficulty)
	for nonce := 0; nonce < maxNonce; nonce++ {
		test := cloneHeader(header)
		test["nonce"] = nonce
		test["difficulty"] = difficulty
		h, err := HashBlockHeader(test)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(h, target) {
			return &PowResult{Nonce: nonce, Hash: h}, nil
		}
	}
	return nil, nil
}

// VerifyPow 校驗區塊 PoW；內部固定 difficulty 字段，避免調用方遺漏。
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
