package node

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"tacm/internal/crypto"
)

// 合約交易以 memo 前綴承載字節碼；value 走正常鏈上交易。
const (
	vmDeployPrefix = "vm:deploy:"
	vmCallPrefix   = "vm:call:"
)

// tx0To0x 把 tx0 錢包地址轉為 EVM 內部 0x 地址（同一 20 字節 hash）。
func tx0To0x(tx0addr string) (string, error) {
	if tx0addr == "" {
		return "", fmt.Errorf("node: 空地址無法轉換")
	}
	h, err := crypto.AddressToHash160(tx0addr)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(h), nil
}

// evmToTx0 把 EVM 0x 地址轉回 tx0 錢包地址。
func evmToTx0(evmAddr string) (string, error) {
	s := strings.TrimPrefix(strings.TrimSpace(evmAddr), "0x")
	b, err := hex.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("node: 0x地址 hex 錯誤: %w", err)
	}
	if len(b) != 20 {
		return "", fmt.Errorf("node: 0x地址應為20字節, got %d", len(b))
	}
	return crypto.Hash160ToAddress(b), nil
}

// hexInput 解析可選 0x 的 hex（奇數補0）。
func hexInput(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return hex.DecodeString(s)
}

// parseAmount 把金額字符串解為整數（小數截斷取整）。
func parseAmount(s string) *big.Int {
	s = strings.TrimSpace(s)
	if s == "" {
		return new(big.Int)
	}
	if v, ok := new(big.Int).SetString(s, 10); ok {
		return v
	}
	f, _, err := big.ParseFloat(s, 0, 256, big.ToNearestEven)
	if err != nil {
		return new(big.Int)
	}
	i, _ := f.Int(nil)
	return i
}

func isContractMemo(memo string) bool {
	return strings.HasPrefix(memo, vmDeployPrefix) ||
		strings.HasPrefix(memo, vmCallPrefix)
}
