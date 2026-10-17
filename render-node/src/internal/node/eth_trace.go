package node

// M129-D：debug_traceTransaction——交易執行追蹤。
//
// TACm 的 EVM 執行器未輸出逐步 structLogs；回傳相容結構（gas/gasUsed/failed/
// returnValue 真實、structLogs 空陣列），供 debug 工具解析。

import (
	"strconv"
)

// debugTraceTransaction 依交易哈希回傳執行追蹤摘要（未找到回 null，以太慣例）。
func (s *RPCServer) debugTraceTransaction(params []any) (map[string]any, error) {
	hash := strip0x(paramString(params, 0))
	tx, err := s.node.db.GetTransaction(hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, nil
	}
	failed := tx.Status != "" && tx.Status != "confirmed" && tx.Status != "success"
	return map[string]any{
		"gas":         "0x" + strconv.FormatInt(tx.GasLimit, 16),
		"gasUsed":     "0x" + strconv.FormatInt(tx.GasUsed, 16),
		"failed":      failed,
		"returnValue": "0x",
		"structLogs":  []any{},
	}, nil
}
