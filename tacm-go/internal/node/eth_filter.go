package node

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"tacm/internal/chaindb"
)

// ethFilter 以太式過濾器（M128）：log / block / pending 三型。
// LastBlock 記錄上次已檢查的鏈頂高度，getFilterChanges 據此回增量。
type ethFilter struct {
	Kind      string
	Addresses []string
	Topics    [][]string
	FromBlock int64
	LastBlock int64
	Created   time.Time
}

// registerFilter 註冊過濾器並回傳 0x 前綴 id（UnixNano 十六進位，衝突重試）。
func (s *RPCServer) registerFilter(f *ethFilter) string {
	s.filtersMu.Lock()
	defer s.filtersMu.Unlock()
	id := ""
	for {
		id = fmt.Sprintf("0x%x", time.Now().UnixNano())
		if _, exists := s.filters[id]; !exists {
			break
		}
	}
	s.filters[id] = f
	return id
}

// ethNewFilter 建立 log 過濾器（Ethereum Filter API 標準）。
func (s *RPCServer) ethNewFilter(params []any) (any, error) {
	filterObj, ok := params[0].(map[string]any)
	if !ok {
		return nil, errors.New("eth: params[0] 需為 filter object")
	}
	f := &ethFilter{Kind: "log", Created: time.Now()}
	latest, err := s.node.db.GetLatestBlock()
	if err == nil && latest != nil {
		if fb := getStringFieldAny(filterObj, "fromBlock"); fb != "" {
			f.FromBlock = parseEthBlock(fb, latest.Height)
		} else {
			f.FromBlock = latest.Height
		}
		// block filter / pending filter 初創即錨定當前鏈頂，只回其後的新資料。
		f.LastBlock = latest.Height
	}
	if a := filterObj["address"]; a != nil {
		switch av := a.(type) {
		case string:
			f.Addresses = append(f.Addresses, normalizeEth0x(av))
		case []any:
			for _, item := range av {
				if s0, ok := item.(string); ok {
					f.Addresses = append(f.Addresses, normalizeEth0x(s0))
				}
			}
		}
	}
	if ts := filterObj["topics"]; ts != nil {
		if arr, ok := ts.([]any); ok {
			f.Topics = parseEthTopics(arr)
		}
	}
	return s.registerFilter(f), nil
}

// ethNewBlockFilter 建立新區塊過濾器。
func (s *RPCServer) ethNewBlockFilter() (any, error) {
	f := &ethFilter{Kind: "block", Created: time.Now()}
	if latest, err := s.node.db.GetLatestBlock(); err == nil && latest != nil {
		f.LastBlock = latest.Height
	}
	return s.registerFilter(f), nil
}

// ethNewPendingTransactionFilter 建立 pending 交易過濾器。
func (s *RPCServer) ethNewPendingTransactionFilter() (any, error) {
	f := &ethFilter{Kind: "pending", Created: time.Now()}
	return s.registerFilter(f), nil
}

// ethUninstallFilter 移除過濾器；回是否存在。
func (s *RPCServer) ethUninstallFilter(params []any) (any, error) {
	id := paramString(params, 0)
	s.filtersMu.Lock()
	defer s.filtersMu.Unlock()
	if _, ok := s.filters[id]; ok {
		delete(s.filters, id)
		return true, nil
	}
	return false, nil
}

// ethGetFilterLogs 回 log 過濾器全部匹配日誌（不更新增量游標）。
func (s *RPCServer) ethGetFilterLogs(params []any) (any, error) {
	id := paramString(params, 0)
	s.filtersMu.Lock()
	f, ok := s.filters[id]
	s.filtersMu.Unlock()
	if !ok {
		return nil, errors.New("eth: filter not found")
	}
	if f.Kind != "log" {
		return nil, errors.New("eth: filter is not a log filter")
	}
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return []any{}, nil
	}
	logs, err := s.node.db.GetLogs(chaindb.LogFilter{
		FromBlock: f.FromBlock,
		ToBlock:   latest.Height,
		Addresses: f.Addresses,
		Topics:    f.Topics,
		Limit:     10000,
	})
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(logs))
	for i := range logs {
		out = append(out, ethLogObject(&logs[i]))
	}
	return out, nil
}

// ethGetFilterChanges 回過濾器自上次查詢以來的增量結果。
func (s *RPCServer) ethGetFilterChanges(params []any) (any, error) {
	id := paramString(params, 0)
	s.filtersMu.Lock()
	f, ok := s.filters[id]
	s.filtersMu.Unlock()
	if !ok {
		return nil, errors.New("eth: filter not found")
	}
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return []any{}, nil
	}
	switch f.Kind {
	case "block":
		out := make([]any, 0)
		for h := f.LastBlock + 1; h <= latest.Height; h++ {
			if b, berr := s.node.db.GetBlock(h); berr == nil && b != nil {
				out = append(out, "0x"+b.Hash)
			}
		}
		if latest.Height > f.LastBlock {
			f.LastBlock = latest.Height
		}
		return out, nil
	case "pending":
		pend, perr := s.node.db.GetMempoolPending(1000)
		if perr != nil {
			return nil, perr
		}
		out := make([]any, 0, len(pend))
		for _, pt := range pend {
			if h, ok := pt["tx_hash"].(string); ok && h != "" {
				out = append(out, "0x"+h)
			}
		}
		return out, nil
	default: // log
		start := f.LastBlock + 1
		if start < f.FromBlock {
			start = f.FromBlock
		}
		if start > latest.Height {
			return []any{}, nil
		}
		logs, lerr := s.node.db.GetLogs(chaindb.LogFilter{
			FromBlock: start,
			ToBlock:   latest.Height,
			Addresses: f.Addresses,
			Topics:    f.Topics,
			Limit:     10000,
		})
		if lerr != nil {
			return nil, lerr
		}
		out := make([]any, 0, len(logs))
		for i := range logs {
			out = append(out, ethLogObject(&logs[i]))
		}
		if latest.Height > f.LastBlock {
			f.LastBlock = latest.Height
		}
		return out, nil
	}
}

// parseEthTopics 解析 filter 的 topics 參數：每層可為單值或陣列（層內 OR）。
func parseEthTopics(arr []any) [][]string {
	out := make([][]string, 0, len(arr))
	for _, layer := range arr {
		var vals []string
		switch lv := layer.(type) {
		case string:
			vals = append(vals, normalizeEth0x(lv))
		case []any:
			for _, item := range lv {
				if s0, ok := item.(string); ok {
					vals = append(vals, normalizeEth0x(s0))
				}
			}
		}
		out = append(out, vals)
	}
	return out
}

// ethFeeHistory 回 EIP-1559 手續費歷史（錢包 gas 建議用）。
// baseFeePerGas 取區塊實際 BaseFee（缺省以 eth_gasPrice 近似）；
// gasUsedRatio 為 gas 使用率；reward 回空（TACm 無 priority 獎勵歷史）。
func (s *RPCServer) ethFeeHistory(params []any) (any, error) {
	blockCount := int64(0)
	if len(params) > 0 {
		switch v := params[0].(type) {
		case float64:
			blockCount = int64(v)
		case string:
			if n, err := strconvParseHex(v); err == nil {
				blockCount = n
			}
		}
	}
	if blockCount < 1 || blockCount > 1024 {
		return nil, errors.New("eth: blockCount 需在 1..1024")
	}
	newestTag := paramString(params, 1)
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return nil, errors.New("eth: 無區塊")
	}
	newest := latest.Height
	if newestTag != "latest" && newestTag != "" && newestTag != "pending" {
		if h := parseEthBlock(newestTag, latest.Height); h >= 0 {
			newest = h
		}
	}
	gp, gerr := s.ethGasPrice()
	if gerr != nil {
		gp = "0x0"
	}
	baseFee := gp
	oldest := newest - blockCount + 1
	if oldest < 0 {
		oldest = 0
	}
	baseFees := make([]any, 0, blockCount)
	gasRatios := make([]float64, 0, blockCount)
	for h := oldest; h <= newest; h++ {
		fee := baseFee
		ratio := float64(0)
		if b, berr := s.node.db.GetBlock(h); berr == nil && b != nil {
			if b.BaseFee != "" {
				fee = "0x" + strings.TrimPrefix(b.BaseFee, "0x")
			}
			if b.GasLimit > 0 {
				ratio = float64(b.GasUsed) / float64(b.GasLimit)
			}
		}
		baseFees = append(baseFees, fee)
		gasRatios = append(gasRatios, ratio)
	}
	return map[string]any{
		"oldestBlock":   hexBig(oldest),
		"baseFeePerGas": baseFees,
		"gasUsedRatio":  gasRatios,
		"reward":        make([]any, len(baseFees)),
	}, nil
}

// ethGetBlockTransactionCountByNumber 回區塊內交易數（hex）。
func (s *RPCServer) ethGetBlockTransactionCountByNumber(params []any) (any, error) {
	tag := paramString(params, 0)
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return nil, errors.New("eth: 無區塊")
	}
	h := latest.Height
	if tag != "latest" && tag != "" && tag != "pending" {
		if hh := parseEthBlock(tag, latest.Height); hh >= 0 {
			h = hh
		}
	}
	txs, err := s.node.db.GetTransactionsByBlock(h)
	if err != nil {
		return nil, err
	}
	return hexBig(int64(len(txs))), nil
}

// ethGetBlockTransactionCountByHash 回指定區塊（by hash）交易數（hex）。
func (s *RPCServer) ethGetBlockTransactionCountByHash(params []any) (any, error) {
	hash := strings.TrimPrefix(paramString(params, 0), "0x")
	b, err := s.node.db.GetBlockByHash(hash)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("eth: 無此區塊")
	}
	txs, err := s.node.db.GetTransactionsByBlock(b.Height)
	if err != nil {
		return nil, err
	}
	return hexBig(int64(len(txs))), nil
}

// ethGetTransactionByBlockNumberAndIndex 依區塊編號＋交易索引取交易。
func (s *RPCServer) ethGetTransactionByBlockNumberAndIndex(params []any) (any, error) {
	tag := paramString(params, 0)
	idx := paramHexInt(params, 1)
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return nil, errors.New("eth: 無區塊")
	}
	h := latest.Height
	if tag != "latest" && tag != "" && tag != "pending" {
		if hh := parseEthBlock(tag, latest.Height); hh >= 0 {
			h = hh
		}
	}
	return s.txByBlockIndex(h, idx)
}

// ethGetTransactionByBlockHashAndIndex 依區塊 hash＋交易索引取交易。
func (s *RPCServer) ethGetTransactionByBlockHashAndIndex(params []any) (any, error) {
	hash := strings.TrimPrefix(paramString(params, 0), "0x")
	idx := paramHexInt(params, 1)
	b, err := s.node.db.GetBlockByHash(hash)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("eth: 無此區塊")
	}
	return s.txByBlockIndex(b.Height, idx)
}

func (s *RPCServer) txByBlockIndex(height, idx int64) (any, error) {
	txs, err := s.node.db.GetTransactionsByBlock(height)
	if err != nil {
		return nil, err
	}
	if idx < 0 || idx >= int64(len(txs)) {
		return nil, nil // 標準語意：索引超出回 null
	}
	return s.ethTxObject(txs[idx]), nil
}

// ethGetUncleCountByBlockNumber / ethGetUncleCountByBlockHash 回 0x0（TACm 無 uncle 區塊）。
func (s *RPCServer) ethGetUncleCountByBlockNumber(params []any) (any, error) {
	return hexBig(0), nil
}

func (s *RPCServer) ethGetUncleCountByBlockHash(params []any) (any, error) {
	return hexBig(0), nil
}

// strconvParseHex 解析 0x 前綴十六進位字串（feeHistory blockCount 用）。
func strconvParseHex(v string) (int64, error) {
	t := strings.TrimPrefix(v, "0x")
	if t == "" {
		return 0, errors.New("eth: 空 hex")
	}
	n, ok := new(big.Int).SetString(t, 16)
	if !ok {
		return 0, errors.New("eth: 非法 hex")
	}
	return n.Int64(), nil
}

// paramHexInt 取 params[i]（hex 字串或數值）轉 int64；缺失/非法回 0。
func paramHexInt(params []any, i int) int64 {
	if i >= len(params) {
		return 0
	}
	switch v := params[i].(type) {
	case float64:
		return int64(v)
	case string:
		if n, err := strconvParseHex(v); err == nil {
			return n
		}
	}
	return 0
}
