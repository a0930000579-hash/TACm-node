package node

import (
	"encoding/json"
	"fmt"
	"strconv"

	"tacm/internal/chaindb"
)

// getString 從 map 取字符串字段（缺失或類型不符返回空串）。
func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

// getInt64 從 map 取整數字段（兼容 float64/int/string）。
func getInt64(m map[string]any, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

// txFromMap 將內存池交易 map 轉為 chaindb.Transaction。
func txFromMap(m map[string]any) chaindb.Transaction {
	return chaindb.Transaction{
		TxHash:      getString(m, "tx_hash"),
		FromAddr:    getString(m, "from"),
		ToAddr:      getString(m, "to"),
		Amount:      getString(m, "amount"),
		Fee:         getString(m, "fee"),
		GasLimit:    getInt64(m, "gas_limit"),
		MaxFee:      getString(m, "max_fee"),
		PriorityFee: getString(m, "priority_fee"),
		Burned:      getString(m, "burned"),
		Nonce:       getInt64(m, "nonce"),
		Ts:          getInt64(m, "ts"),
		Signature:   getString(m, "signature"),
		Pubkey:      getString(m, "pubkey"),
		Memo:        getString(m, "memo"),
	}
}
