package node

// M129-D：debug_traceTransaction——交易執行追蹤。
// M151e：debug_traceCall——未上鏈的模擬呼叫追蹤（hardhat/viem/Remix 除錯流程必備）。
//
// v137：debug_traceCall/trace_call 執行 VM 並輸出真實逐 opcode structLogs
// （pc/op/opName/gas/gasCost/depth/stack）；debug_traceTransaction/trace_block
// 基於 DB 摘要（gas/gasUsed/failed 真實），structLogs 保留空（無歷史狀態重放）。

import (
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/vm"
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

// debugTraceCall 模擬執行呼叫並回追蹤摘要（params[0] = call object，同 eth_call）。
func (s *RPCServer) debugTraceCall(params []any) (map[string]any, error) {
	callObj, ok := params[0].(map[string]any)
	if !ok {
		return nil, errors.New("eth: params[0] 需為 call object")
	}
	to := getStringFieldAny(callObj, "to")
	from := getStringFieldAny(callObj, "from")
	data := getStringFieldAny(callObj, "data")
	toTx0, err := s.node.normalizeEthAddr(to)
	if err != nil {
		return nil, err
	}
	to0x, err := tx0To0x(toTx0)
	if err != nil {
		return nil, err
	}
	caller0x := "0x0000000000000000000000000000000000000000"
	if from != "" {
		fTx0, ferr := s.node.normalizeEthAddr(from)
		if ferr != nil {
			return nil, ferr
		}
		caller0x, err = tx0To0x(fTx0)
		if err != nil {
			return nil, err
		}
	}
	calldata, err := hexInput(data)
	if err != nil {
		return nil, errors.New("eth: data hex 錯誤")
	}
	callValue := new(big.Int)
	if v := getStringFieldAny(callObj, "value"); v != "" {
		callValue, _ = new(big.Int).SetString(strings.TrimPrefix(v, "0x"), 16)
		if callValue == nil {
			return nil, errors.New("eth: value 解析失敗")
		}
	}
	r, err := s.node.contracts.SimulateCallGasOverrides(to0x, caller0x, calldata, callValue, s.node.contracts.DefaultGas(), nil, true)
	if err != nil {
		return nil, err
	}
	steps := structLogsSteps(r.Steps)
	if steps == nil {
		steps = []any{}
	}
	return map[string]any{
		"gas":         "0x" + strconv.FormatUint(s.node.contracts.DefaultGas(), 16),
		"gasUsed":     "0x" + strconv.FormatUint(r.GasUsed, 16),
		"failed":      !r.OK || r.Reverted,
		"returnValue": "0x" + strings.TrimPrefix(r.ReturnData, "0x"),
		"structLogs":  steps,
	}, nil
}

// structLogsSteps 將 VM 收集的 ExecStep 轉 geth 風格 structLogs（pc/op/opName/gas/gasCost/depth/stack）。
func structLogsSteps(steps []*vm.ExecStep) []any {
	if len(steps) == 0 {
		return nil
	}
	out := make([]any, 0, len(steps))
	for _, st := range steps {
		stack := []any{}
		for _, sv := range st.Stack {
			stack = append(stack, sv)
		}
		out = append(out, map[string]any{
			"pc":      "0x" + strconv.FormatUint(st.PC, 16),
			"op":      st.Op,
			"opName":  st.OpName,
			"gas":     "0x" + strconv.FormatUint(st.Gas, 16),
			"gasCost": "0x" + strconv.FormatUint(st.GasCost, 16),
			"depth":   st.Depth,
			"stack":   stack,
		})
	}
	return out
}

// debugTraceBlock（C1）依區塊高度（debug_traceBlockByNumber）或哈希
// （debug_traceBlockByHash）回區塊內全部交易的執行追蹤摘要陣列。
// 與 debug_traceTransaction 同構（gas/gasUsed/failed/returnValue 真實、
// structLogs 空），供主流 debug 工具（hardhat/tenderly 等）解析。
func (s *RPCServer) debugTraceBlock(params []any, byHash bool) ([]map[string]any, error) {
	ref := paramString(params, 0)
	if ref == "" {
		return nil, errors.New("debug: 缺少區塊參數")
	}
	var b *chaindb.Block
	if byHash {
		bb, err := s.node.db.GetBlockByHash(strip0x(ref))
		if err != nil || bb == nil {
			return nil, errors.New("debug: 區塊不存在: " + ref)
		}
		b = bb
	} else {
		latest, err := s.node.db.GetLatestBlock()
		if err != nil || latest == nil {
			return nil, errors.New("debug: 無區塊")
		}
		if ref == "latest" || ref == "pending" || ref == "" {
			b = latest
		} else if h := parseEthBlock(ref, latest.Height); h >= 0 {
			bb, berr := s.node.db.GetBlock(h)
			if berr != nil || bb == nil {
				return nil, errors.New("debug: 區塊不存在: " + ref)
			}
			b = bb
		} else {
			return nil, errors.New("debug: 區塊參數無法解析: " + ref)
		}
	}
	txs, err := s.node.db.GetTransactionsByBlock(b.Height)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(txs))
	for i := range txs {
		out = append(out, s.traceTxSummary(txs[i]))
	}
	return out, nil
}

// traceTxSummary 單筆交易追蹤摘要（debug_traceTransaction / traceBlock 共用）。
func (s *RPCServer) traceTxSummary(tx *chaindb.Transaction) map[string]any {
	failed := tx.Status != "" && tx.Status != "confirmed" && tx.Status != "success"
	return map[string]any{
		"gas":         "0x" + strconv.FormatInt(tx.GasLimit, 16),
		"gasUsed":     "0x" + strconv.FormatInt(tx.GasUsed, 16),
		"failed":      failed,
		"returnValue": "0x",
		"structLogs":  []any{},
	}
}

// traceFilter 實作 Parity/OpenEthereum 風格 trace_filter：
// 依區塊範圍 + 位址過濾 + after/count 分頁回傳歷史交易 trace 清單。
// v135：Etherscan 級索引基建（鏈上合約/位址活動查詢）。
// 過濾物件：{fromBlock, toBlock, fromAddress:[], toAddress:[], after, count}
func (s *RPCServer) traceFilter(params []any) ([]map[string]any, error) {
	f, ok := params[0].(map[string]any)
	if !ok {
		return nil, errors.New("trace: params[0] 需為 filter object")
	}
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return nil, errors.New("trace: 無區塊")
	}
	fromN := int64(0)
	toN := latest.Height
	if v := getStringFieldAny(f, "fromBlock"); v != "" {
		if n := parseEthBlock(v, latest.Height); n >= 0 {
			fromN = int64(n)
		}
	}
	if v := getStringFieldAny(f, "toBlock"); v != "" {
		if n := parseEthBlock(v, latest.Height); n >= 0 {
			toN = int64(n)
		}
	}
	if toN < fromN {
		toN = fromN
	}
	// 掃描範圍上限（防灌）：單次最多 10000 區塊。
	if toN-fromN > 10000 {
		toN = fromN + 10000
	}
	fromAddrs := stringSliceField(f, "fromAddress")
	toAddrs := stringSliceField(f, "toAddress")
	after := intField(f, "after")
	count := intField(f, "count")
	if count <= 0 {
		count = 100
	}
	if count > 1000 {
		count = 1000
	}
	fromSet := s.normSet(fromAddrs)
	toSet := s.normSet(toAddrs)

	var out []map[string]any
	for h := fromN; h <= toN && int64(len(out)) < int64(after)+int64(count); h++ {
		b, berr := s.node.db.GetBlock(h)
		if berr != nil || b == nil {
			continue
		}
		txs, terr := s.node.db.GetTransactionsByBlock(h)
		if terr != nil {
			continue
		}
		for i := range txs {
			tx := txs[i]
			if len(fromSet) > 0 && !fromSet[tx.FromAddr] {
				continue
			}
			if len(toSet) > 0 && !toSet[tx.ToAddr] {
				continue
			}
			if len(out) < after {
				out = append(out, nil) // 僅消耗分頁偏移，不輸出
				continue
			}
			out = append(out, s.parityTrace(tx))
			if len(out) >= after+count {
				break
			}
		}
	}
	// 移除分頁偏移佔位（若有）。
	res := make([]map[string]any, 0, len(out))
	for _, tr := range out {
		if tr != nil {
			res = append(res, tr)
		}
	}
	return res, nil
}

// parityTrace 單筆交易轉 Parity trace 結構（type=call/create）。
func (s *RPCServer) parityTrace(tx *chaindb.Transaction) map[string]any {
	isCreate := tx.ToAddr == "" || tx.ToAddr == "0x"
	ttype := "call"
	from0x, ferr := tx0To0x(tx.FromAddr)
	if ferr != nil {
		from0x = "0x" + tx.FromAddr
	}
	actionTo := ""
	if !isCreate {
		to0x, toerr := tx0To0x(tx.ToAddr)
		if toerr != nil {
			to0x = "0x" + tx.ToAddr
		}
		actionTo = to0x
	}
	if isCreate {
		ttype = "create"
	}
	value := "0x0"
	if tx.Amount != "" {
		if strings.HasPrefix(tx.Amount, "0x") {
			value = "0x" + strings.TrimPrefix(tx.Amount, "0x")
		} else if n, ok := new(big.Int).SetString(tx.Amount, 10); ok {
			value = "0x" + n.Text(16)
		}
	}
	gas := "0x" + strconv.FormatInt(tx.GasLimit, 16)
	gasUsed := "0x" + strconv.FormatInt(tx.GasUsed, 16)
	bn := "0x" + strconv.FormatInt(tx.BlockHeight, 16)
	bh := "0x" + tx.BlockHash
	th := "0x" + tx.TxHash
	failed := tx.Status != "" && tx.Status != "confirmed" && tx.Status != "success"
	action := map[string]any{
		"callType": "call", "from": from0x,
		"to": actionTo, "value": value, "gas": gas,
		"input": "0x", "revertReason": nil,
	}
	if isCreate {
		action["callType"] = "create"
	}
	result := map[string]any{"gasUsed": gasUsed, "output": "0x"}
	if failed {
		result["error"] = "reverted"
	}
	return map[string]any{
		"action": action, "blockHash": bh, "blockNumber": bn,
		"result": result, "subtraces": 0, "traceAddress": []any{},
		"transactionHash": th, "transactionPosition": tx.TxIndex, "type": ttype,
	}
}

func stringSliceField(m map[string]any, key string) []string {
	var out []string
	switch v := m[key].(type) {
	case []any:
		for _, it := range v {
			if s, ok := it.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case string:
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// normSet 將過濾位址（0x 或 tx0）標準化為鏈內部格式（tx0）後建集合。
func (s *RPCServer) normSet(addrs []string) map[string]bool {
	set := map[string]bool{}
	for _, a := range addrs {
		norm, err := s.node.normalizeEthAddr(a)
		if err != nil {
			continue
		}
		set[norm] = true
	}
	return set
}


// debugGetRawReceipts 依區塊哈希回區塊內全部交易收據原始 JSON（geth debug 面）。
func (s *RPCServer) debugGetRawReceipts(params []any) ([]map[string]any, error) {
	ref := paramString(params, 0)
	if ref == "" {
		return nil, errors.New("debug: 缺少區塊參數")
	}
	var b *chaindb.Block
	if strings.HasPrefix(ref, "0x") && len(ref) == 66 {
		bb, err := s.node.db.GetBlockByHash(strip0x(ref))
		if err != nil || bb == nil {
			return nil, errors.New("debug: 區塊不存在: " + ref)
		}
		b = bb
	} else {
		latest, err := s.node.db.GetLatestBlock()
		if err != nil || latest == nil {
			return nil, errors.New("debug: 無區塊")
		}
		if ref == "latest" || ref == "pending" || ref == "" {
			b = latest
		} else if h := parseEthBlock(ref, latest.Height); h >= 0 {
			bb, berr := s.node.db.GetBlock(h)
			if berr != nil || bb == nil {
				return nil, errors.New("debug: 區塊不存在: " + ref)
			}
			b = bb
		} else {
			return nil, errors.New("debug: 區塊參數無法解析: " + ref)
		}
	}
	txs, err := s.node.db.GetTransactionsByBlock(b.Height)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(txs))
	for i := range txs {
		r, rerr := s.ethReceiptForTx(txs[i])
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, r)
	}
	return out, nil
}

// TraceFilterPublic 只讀面（nodeweb）用：Parity trace_filter。
func (s *RPCServer) TraceFilterPublic(params []any) ([]map[string]any, error) {
	return s.traceFilter(params)
}

// DebugGetRawReceiptsPublic 只讀面用：區塊全部收據原始 JSON。
func (s *RPCServer) DebugGetRawReceiptsPublic(params []any) ([]map[string]any, error) {
	return s.debugGetRawReceipts(params)
}

// DebugStorageRangeAtPublic 只讀面用：帳號儲存槽枚舉。
func (s *RPCServer) DebugStorageRangeAtPublic(params []any) (map[string]any, error) {
	return s.debugStorageRangeAt(params)
}

// DebugTraceBlockPublic 只讀面用：trace_block / debug_traceBlockByNumber。
func (s *RPCServer) DebugTraceBlockPublic(params []any, byHash bool) ([]map[string]any, error) {
	return s.debugTraceBlock(params, byHash)
}

// DebugTraceCallPublic 只讀面（nodeweb）用：debug_traceCall / trace_call。
func (s *RPCServer) DebugTraceCallPublic(params []any) (map[string]any, error) {
	return s.debugTraceCall(params)
}

// DebugTraceTransactionPublic 只讀面用：debug_traceTransaction。
func (s *RPCServer) DebugTraceTransactionPublic(params []any) (map[string]any, error) {
	return s.debugTraceTransaction(params)
}

// ===== v138：Parity replay 面（trace_transaction / trace_replayTransaction /
// trace_replayBlockTransactions）=====
//
// 以模擬執行（最新世界狀態）重放交易，回 Parity trace 樹（根層 frame＋VM
// call-frame 子樹）。無 state-at-height 能力，模擬為最新狀態近似；stateDiff
// 不計算（回空物件），與主流結構相容。

// traceFrameToParity 單一 TraceFrame → Parity trace 節點（traceAddress/subtraces 由呼叫端填）。
func traceFrameToParity(f *vm.TraceFrame) map[string]any {
	action := map[string]any{
		"callType": f.Type, "from": f.From,
		"value": "0x" + strings.TrimPrefix(f.Value.Text(16), "0x"), "gas": "0x" + strconv.FormatUint(f.Gas, 16),
		"input": "0x" + hex.EncodeToString(f.Input),
	}
	action["to"] = f.To
	if f.Type == "create" {
		delete(action, "to")
	}
	result := map[string]any{"gasUsed": "0x" + strconv.FormatUint(f.GasUsed, 16), "output": "0x" + hex.EncodeToString(f.Output)}
	if f.Reverted {
		result = map[string]any{"gasUsed": "0x" + strconv.FormatUint(f.GasUsed, 16)}
	}
	return map[string]any{
		"action": action, "blockHash": nil, "blockNumber": nil,
		"result": result, "subtraces": 0, "traceAddress": []any{}, "type": f.Type,
	}
}

// parityRootFrame 根層 frame（type=call/create，無 parent）。
func (s *RPCServer) parityRootFrame(tx *chaindb.Transaction, cr *vm.CallResult) map[string]any {
	isCreate := tx.ToAddr == "" || tx.ToAddr == "0x"
	ttype := "call"
	if isCreate {
		ttype = "create"
	}
	from0x, _ := tx0To0x(tx.FromAddr)
	if from0x == "" {
		from0x = "0x" + tx.FromAddr
	}
	to0x := ""
	if !isCreate {
		t0x, terr := tx0To0x(tx.ToAddr)
		if terr != nil {
			t0x = "0x" + tx.ToAddr
		}
		to0x = t0x
	}
	value := "0x0"
	if tx.Amount != "" {
		if strings.HasPrefix(tx.Amount, "0x") {
			value = "0x" + strings.TrimPrefix(tx.Amount, "0x")
		} else if n, ok := new(big.Int).SetString(tx.Amount, 10); ok {
			value = "0x" + n.Text(16)
		}
	}
	gas := "0x" + strconv.FormatInt(tx.GasLimit, 16)
	input := s.txCalldata(tx)
	action := map[string]any{"callType": ttype, "from": from0x, "to": to0x, "value": value, "gas": gas, "input": input}
	if isCreate {
		delete(action, "to")
	}
	out := "0x"
	gasUsed := "0x" + strconv.FormatInt(tx.GasUsed, 16)
	if cr != nil {
		out = "0x" + strings.TrimPrefix(cr.ReturnData, "0x")
		gasUsed = "0x" + strconv.FormatUint(cr.GasUsed, 16)
	}
	result := map[string]any{"gasUsed": gasUsed, "output": out}
	failed := (cr != nil && (cr.Reverted || !cr.OK)) || (cr == nil && tx.Status != "" && tx.Status != "confirmed" && tx.Status != "success")
	if failed {
		result = map[string]any{"gasUsed": gasUsed}
	}
	return map[string]any{"action": action, "result": result, "subtraces": 0, "traceAddress": []any{}, "type": ttype}
}

// txCalldata 從交易 memo 提取 calldata（vm: 前綴或純 hex；無則空）。
func (s *RPCServer) txCalldata(tx *chaindb.Transaction) string {
	m := tx.Memo
	if m == "" {
		return "0x"
	}
	m = strings.TrimPrefix(m, "vm:")
	cd, err := hexInput(m)
	if err != nil {
		return "0x"
	}
	return "0x" + hex.EncodeToString(cd)
}

// parityTraceTree 根層 frame＋子 frame 樹（traceAddress 依 DFS 序，subtraces 依子數）。
func parityTraceTree(root map[string]any, frames []*vm.TraceFrame) []map[string]any {
	out := []map[string]any{root}
	if len(frames) == 0 {
		return out
	}
	children := map[int][]*vm.TraceFrame{}
	idxOf := map[*vm.TraceFrame]int{}
	for i, f := range frames {
		idxOf[f] = i
		children[f.Parent] = append(children[f.Parent], f)
	}
	var walk func(parentIdx int, addr []any)
	walk = func(parentIdx int, addr []any) {
		for _, c := range children[parentIdx] {
			childAddr := append(append([]any{}, addr...), c.Sibling)
			node := traceFrameToParity(c)
			node["traceAddress"] = childAddr
			node["subtraces"] = len(children[idxOf[c]])
			out = append(out, node)
			walk(idxOf[c], childAddr)
		}
	}
	root["subtraces"] = len(children[-1])
	walk(-1, []any{})
	return out
}

// simReplayTx 模擬執行單筆交易，回 parityTraceTree（create 交易無法模擬，回 DB 摘要根層）。
func (s *RPCServer) simReplayTx(tx *chaindb.Transaction) []map[string]any {
	isCreate := tx.ToAddr == "" || tx.ToAddr == "0x"
	var cr *vm.CallResult
	if !isCreate {
		to0x, terr := tx0To0x(tx.ToAddr)
		if terr == nil && len(s.node.contracts.GetCode(to0x)) > 0 {
			caller0x, _ := tx0To0x(tx.FromAddr)
			cd, _ := hexInput(s.txCalldata(tx))
			callValue := new(big.Int)
			if tx.Amount != "" {
				if strings.HasPrefix(tx.Amount, "0x") {
					callValue, _ = new(big.Int).SetString(strings.TrimPrefix(tx.Amount, "0x"), 16)
				} else if n, ok := new(big.Int).SetString(tx.Amount, 10); ok {
					callValue = n
				}
			}
			r, rerr := s.node.contracts.SimulateCallGasOverrides(to0x, caller0x, cd, callValue, uint64(tx.GasLimit), nil, false)
			if rerr == nil {
				cr = r
			}
		}
	}
	var tr []*vm.TraceFrame
	if cr != nil {
		tr = cr.Trace
	}
	return parityTraceTree(s.parityRootFrame(tx, cr), tr)
}

// traceTransaction 單筆交易 trace 樹（trace_transaction；Parity 回 trace 陣列）。
func (s *RPCServer) traceTransaction(params []any) ([]map[string]any, error) {
	hash := strip0x(paramString(params, 0))
	if hash == "" {
		return nil, errors.New("trace: 缺少交易哈希")
	}
	tx, err := s.node.db.GetTransaction(hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, nil
	}
	return s.simReplayTx(tx), nil
}

// traceReplayTransaction 單筆重放（trace_replayTransaction；Parity 回 {output,stateDiff,trace}）。
func (s *RPCServer) traceReplayTransaction(params []any) (map[string]any, error) {
	hash := strip0x(paramString(params, 0))
	if hash == "" {
		return nil, errors.New("trace: 缺少交易哈希")
	}
	tx, err := s.node.db.GetTransaction(hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, nil
	}
	frames := s.simReplayTx(tx)
	out := "0x"
	for _, f := range frames {
		if res, ok := f["result"].(map[string]any); ok {
			if o, ok2 := res["output"].(string); ok2 && o != "" {
				out = o
			}
		}
	}
	return map[string]any{"output": out, "stateDiff": map[string]any{}, "trace": frames}, nil
}

// traceReplayBlockTransactions 區塊內每筆重放（trace_replayBlockTransactions）。
func (s *RPCServer) traceReplayBlockTransactions(params []any) ([]map[string]any, error) {
	ref := paramString(params, 0)
	if ref == "" {
		return nil, errors.New("trace: 缺少區塊參數")
	}
	var b *chaindb.Block
	var err error
	if strings.HasPrefix(strings.ToLower(ref), "0x") {
		b, err = s.node.db.GetBlockByHash(strip0x(ref))
	} else {
		latest, lerr := s.node.db.GetLatestBlock()
		if lerr != nil || latest == nil {
			return nil, errors.New("trace: 無區塊")
		}
		if ref == "latest" || ref == "pending" {
			b = latest
		} else if h := parseEthBlock(ref, latest.Height); h >= 0 {
			b, err = s.node.db.GetBlock(h)
		} else {
			return nil, errors.New("trace: 區塊參數無法解析: " + ref)
		}
	}
	if err != nil || b == nil {
		return nil, errors.New("trace: 區塊不存在: " + ref)
	}
	txs, terr := s.node.db.GetTransactionsByBlock(b.Height)
	if terr != nil {
		return nil, terr
	}
	out := make([]map[string]any, 0, len(txs))
	for i := range txs {
		frames := s.simReplayTx(txs[i])
		o := "0x"
		for _, f := range frames {
			if res, ok := f["result"].(map[string]any); ok {
				if ov, ok2 := res["output"].(string); ok2 && ov != "" {
					o = ov
				}
			}
		}
		out = append(out, map[string]any{"output": o, "stateDiff": map[string]any{}, "trace": frames})
	}
	return out, nil
}

// TraceTransactionPublic 只讀面用：trace_transaction。
func (s *RPCServer) TraceTransactionPublic(params []any) ([]map[string]any, error) {
	return s.traceTransaction(params)
}

// TraceReplayTransactionPublic 只讀面用：trace_replayTransaction。
func (s *RPCServer) TraceReplayTransactionPublic(params []any) (map[string]any, error) {
	return s.traceReplayTransaction(params)
}

// TraceReplayBlockPublic 只讀面用：trace_replayBlockTransactions。
func (s *RPCServer) TraceReplayBlockPublic(params []any) ([]map[string]any, error) {
	return s.traceReplayBlockTransactions(params)
}

// ===== v139：trace_callMany（Parity 批次模擬）＋ trace_rawTransaction（未上鏈交易重放）=====

// traceCallMany 批次模擬呼叫（params[0]=call objects 陣列），逐筆回 trace_call 摘要。
func (s *RPCServer) traceCallMany(params []any) ([]map[string]any, error) {
	arr, ok := params[0].([]any)
	if !ok {
		return nil, errors.New("trace: params[0] 需為 call object 陣列")
	}
	if len(arr) == 0 {
		return []map[string]any{}, nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		co, ok := it.(map[string]any)
		if !ok {
			return nil, errors.New("trace: 呼叫物件需為 object")
		}
		d, derr := s.debugTraceCall([]any{co})
		if derr != nil {
			return nil, derr
		}
		out = append(out, d)
	}
	return out, nil
}

// traceRawTransaction 未上鏈 raw 交易重放（解析→驗簽→模擬），回 {output, stateDiff, trace}。
func (s *RPCServer) traceRawTransaction(params []any) (map[string]any, error) {
	rawHex := paramString(params, 0)
	if rawHex == "" {
		return nil, errors.New("trace: 缺少 raw 交易 hex")
	}
	m, err := s.node.rawHexToTxMap(rawHex)
	if err != nil {
		return nil, err
	}
	ct := &chaindb.Transaction{
		FromAddr: strip0x(getStringFieldAny(m, "from")),
		ToAddr:   strip0x(getStringFieldAny(m, "to")),
		Amount:   getStringFieldAny(m, "amount"),
		Memo:     getStringFieldAny(m, "memo"),
		GasLimit: 0,
		GasUsed:  0,
		Status:   "",
	}
	if gl, ok := m["gas_limit"].(int64); ok {
		ct.GasLimit = gl
	}
	frames := s.simReplayTx(ct)
	out := "0x"
	for _, f := range frames {
		if res, ok2 := f["result"].(map[string]any); ok2 {
			if o, ok3 := res["output"].(string); ok3 && o != "" {
				out = o
			}
		}
	}
	return map[string]any{"output": out, "stateDiff": map[string]any{}, "trace": frames}, nil
}

// TraceCallManyPublic 只讀面用：trace_callMany。
func (s *RPCServer) TraceCallManyPublic(params []any) ([]map[string]any, error) {
	return s.traceCallMany(params)
}

// TraceRawTransactionPublic 只讀面用：trace_rawTransaction。
func (s *RPCServer) TraceRawTransactionPublic(params []any) (map[string]any, error) {
	return s.traceRawTransaction(params)
}
