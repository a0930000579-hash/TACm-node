package node

// M129-B：txpool_status / txpool_content——mempool 檢視（開發者與監控工具常用）。

import (
	"strconv"
)

// txpoolStatus 回報內存池統計：pending＝待打包交易數、queued＝排隊數（本鏈統一佇列，
// 依 nonce 排序打包，queued 恆為 0，語意相容）。
func (s *RPCServer) txpoolStatus() map[string]string {
	size := s.node.db.MempoolSize()
	hex := "0x" + strconv.FormatInt(size, 16)
	return map[string]string{"pending": hex, "queued": "0x0"}
}

// txpoolContent 回報內存池交易明細：pending 按地址分組（from → nonce → 交易），queued 空。
func (s *RPCServer) txpoolContent() (map[string]any, error) {
	txs, err := s.node.db.MempoolTxs()
	if err != nil {
		return nil, err
	}
	pending := make(map[string]any)
	for _, tx := range txs {
		from, _ := tx.Payload["from"].(string)
		if from == "" {
			continue
		}
		nonce := ""
		if v, ok := tx.Payload["nonce"]; ok {
			nonce = "0x" + strconv.FormatInt(toInt64(v), 16)
		}
		// 主流 txpool_content 結構：pending[from][nonce] = {hash, nonce, from, to, value, gasPrice, ...}
		entry := map[string]any{
			"hash":  tx.TxHash,
			"nonce": nonce,
			"from":  from,
			"input": "0x",
		}
		if to, ok := tx.Payload["to"].(string); ok {
			entry["to"] = to
		}
		if amt, ok := tx.Payload["amount"].(string); ok {
			entry["value"] = amt
		}
		if fee, ok := tx.Payload["fee"].(string); ok {
			entry["gasPrice"] = fee
		}
		if tx.Fee != "" {
			entry["gasPrice"] = tx.Fee
		}
		perNonce := pending[from]
		if perNonce == nil {
			perNonce = make(map[string]any)
			pending[from] = perNonce
		}
		perNonce.(map[string]any)[nonce] = entry
	}
	return map[string]any{"pending": pending, "queued": map[string]any{}}, nil
}

// txpoolInspect（C2）回報內存池交易摘要：pending[from][nonce] = {hash, from, to, value, gas, gasPrice, input, nonce}。
// 主流面（Etherscan 等監控工具）以字串欄位檢視待打包交易，語意與 txpool_content 同源。
func (s *RPCServer) txpoolInspect() (map[string]any, error) {
	txs, err := s.node.db.MempoolTxs()
	if err != nil {
		return nil, err
	}
	pending := make(map[string]any)
	for _, tx := range txs {
		from, _ := tx.Payload["from"].(string)
		if from == "" {
			continue
		}
		nonce := "0x0"
		if v, ok := tx.Payload["nonce"]; ok {
			nonce = "0x" + strconv.FormatInt(toInt64(v), 16)
		}
		entry := map[string]any{
			"hash":     tx.TxHash,
			"nonce":    nonce,
			"from":     from,
			"to":       "",
			"value":    "0x0",
			"gas":      "0x0",
			"gasPrice": "0x0",
			"input":    "0x",
		}
		if to, ok := tx.Payload["to"].(string); ok {
			entry["to"] = to
		}
		if amt, ok := tx.Payload["amount"].(string); ok {
			entry["value"] = amt
		}
		if fee, ok := tx.Payload["fee"].(string); ok {
			entry["gasPrice"] = fee
		}
		if tx.Fee != "" {
			entry["gasPrice"] = tx.Fee
		}
		if gl, ok := tx.Payload["gas_limit"].(string); ok && gl != "" {
			entry["gas"] = gl
		}
		perNonce := pending[from]
		if perNonce == nil {
			perNonce = make(map[string]any)
			pending[from] = perNonce
		}
		perNonce.(map[string]any)[nonce] = entry
	}
	return map[string]any{"pending": pending, "queued": map[string]any{}}, nil
}

// toInt64 把 any（string/float64/int64）轉 int64（mempool nonce 顯示用）。
func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

