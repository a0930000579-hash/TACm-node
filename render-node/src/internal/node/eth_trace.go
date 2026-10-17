package node

// M129-D：debug_traceTransaction——交易執行追蹤。
// M151e：debug_traceCall——未上鏈的模擬呼叫追蹤（hardhat/viem/Remix 除錯流程必備）。
//
// TACm 的 EVM 執行器未輸出逐步 structLogs；回傳相容結構（gas/gasUsed/failed/
// returnValue 真實、structLogs 空陣列），供 debug 工具解析。

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
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
	r, err := s.node.contracts.SimulateCallGas(to0x, caller0x, calldata, callValue, s.node.contracts.DefaultGas())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"gas":         "0x" + strconv.FormatUint(s.node.contracts.DefaultGas(), 16),
		"gasUsed":     "0x" + strconv.FormatUint(r.GasUsed, 16),
		"failed":      !r.OK || r.Reverted,
		"returnValue": "0x" + strings.TrimPrefix(r.ReturnData, "0x"),
		"structLogs":  []any{},
	}, nil
}
