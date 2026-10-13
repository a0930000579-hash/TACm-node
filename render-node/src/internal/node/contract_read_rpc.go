package node

// M139 合約驗證中心：合約 read 互動 RPC。
// POST /contract/read
// body: {"address":"tx0...","method_sig":"balanceOf(address)","inputs":["address"],"args":["0x..."],"caller":"tx0...(選填)"}
// 回: {"outputs":[...],"return_data":"0x...","gas_used":N,"reverted":false,"error":""}

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"tacm/internal/vm"
)

type contractReadReq struct {
	Address    string   `json:"address"`
	MethodSig  string   `json:"method_sig"`
	Inputs     []string `json:"inputs"`
	Args       []any    `json:"args"`
	Caller     string   `json:"caller,omitempty"`
	CallValue  string   `json:"call_value,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`
	ForceGas   *uint64  `json:"gas,omitempty"`
}

// handleContractRead 以 ABI 編碼參數 + 模擬呼叫執行只讀合約互動。
func (s *RPCServer) handleContractRead(w http.ResponseWriter, r *http.Request) {
	var req contractReadReq
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	if req.Address == "" || req.MethodSig == "" {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "address and method_sig required"})
		return
	}
	to0x, err := normalizeAddrTo0x(req.Address)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	// 合約必須存在
	if len(s.node.contracts.GetCode(to0x)) == 0 {
		writeJSON(w, http.StatusNotFound,
			map[string]string{"error": "contract not found or empty code"})
		return
	}
	// 參數數量校驗（方法簽名內的參數型別 vs inputs）
	sigTypes := parseSigInputs(req.MethodSig)
	if len(sigTypes) > 0 && len(req.Inputs) > 0 && len(sigTypes) != len(req.Inputs) {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": fmt.Sprintf("method %q 需要 %d 個參數，給 %d", req.MethodSig, len(sigTypes), len(req.Inputs))})
		return
	}
	types := req.Inputs
	if len(types) == 0 {
		types = sigTypes
	}
	if len(types) != len(req.Args) {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": fmt.Sprintf("參數數量 %d 與型別 %d 不符", len(req.Args), len(types))})
		return
	}
	// address 型別參數正規化：接受 tx0 或 0x 格式。
	convArgs := make([]any, len(req.Args))
	for i, a := range req.Args {
		if strings.Contains(types[i], "address") {
			if s, ok := a.(string); ok {
				x, xerr := normalizeAddrTo0x(s)
				if xerr != nil {
					writeJSON(w, http.StatusBadRequest,
						map[string]string{"error": fmt.Sprintf("參數 %d address 無效: %v", i, xerr)})
					return
				}
				convArgs[i] = x
				continue
			}
		}
		convArgs[i] = a
	}
	calldata, err := vm.EncodeArgs(types, convArgs)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errBody(err))
		return
	}
	sel, serr := vm.FuncSelector(req.MethodSig)
	if serr != nil {
		writeJSON(w, http.StatusBadRequest, errBody(serr))
		return
	}
	full := append(sel, calldata...)

	caller0x := "0x0000000000000000000000000000000000000000"
	if req.Caller != "" {
		cTx0, cerr := s.node.normalizeEthAddr(req.Caller)
		if cerr != nil {
			writeJSON(w, http.StatusBadRequest, errBody(cerr))
			return
		}
		caller0x, err = tx0To0x(cTx0)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errBody(err))
			return
		}
	}
	callValue := new(big.Int)
	if req.CallValue != "" {
		callValue = parseAmount(req.CallValue)
	}
	gas := s.node.contracts.DefaultGas()
	if req.ForceGas != nil {
		gas = *req.ForceGas
	}
	res, err := s.node.contracts.SimulateCallGas(to0x, caller0x, full, callValue, gas)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(err))
		return
	}
	if res.Reverted || !res.OK {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":           false,
			"reverted":     res.Reverted,
			"revert_reason": res.RevertReason,
			"error":        res.Error,
			"gas_used":     res.GasUsed,
		})
		return
	}
	returnData, herr := hex.DecodeString(strings.TrimPrefix(res.ReturnData, "0x"))
	if herr != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(fmt.Errorf("return data hex 無效: %w", herr)))
		return
	}
	// outputs 缺省時回原始 return_data，不嘗試解碼
	if len(req.Outputs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"outputs":     []any{},
			"return_data": res.ReturnData,
			"gas_used":    res.GasUsed,
		})
		return
	}
	decoded, derr := vm.DecodeOutputs(req.Outputs, returnData)
	if derr != nil && len(returnData) == 0 {
		// 空回傳且要求 string：回空字串
		decoded = []any{""}
		derr = nil
	}
	if derr != nil {
		// 本鏈 VM 簡化慣例 fallback：單一 string 回傳為 32-byte ASCII word
		// （M44 標準代幣 name/symbol 以 MSTORE@0+RETURN(0,32) 產出）。
		if len(req.Outputs) == 1 && req.Outputs[0] == "string" && len(returnData) <= 32 {
			// 本鏈 RET32 慣例：值以 MSTORE@0 產出，word 內低位右對齊，前/尾皆可能補零。
			raw := strings.Trim(string(returnData), "\x00")
			ascii := true
			for _, c := range raw {
				if c < 0x20 || c > 0x7e {
					ascii = false
					break
				}
			}
			if ascii {
				decoded = []any{raw}
				derr = nil
			}
		}
	}
	if derr != nil {
		writeJSON(w, http.StatusInternalServerError, errBody(derr))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"outputs":     decoded,
		"return_data": res.ReturnData,
		"gas_used":    res.GasUsed,
	})
}

// normalizeAddrTo0x 接受 tx0 或 0x 格式地址，回 EVM 內部 0x 地址。
func normalizeAddrTo0x(a string) (string, error) {
	a = strings.TrimSpace(a)
	if strings.HasPrefix(a, "0x") || strings.HasPrefix(a, "0X") {
		s := strings.TrimPrefix(strings.TrimPrefix(a, "0x"), "0X")
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != 20 {
			return "", fmt.Errorf("0x 地址無效 %q", a)
		}
		return "0x" + hex.EncodeToString(b), nil
	}
	return tx0To0x(a)
}

// parseSigInputs 從方法簽名 "name(type1,type2)" 抽出參數型別列表。
// 解析失敗回 nil（前端改以 inputs 欄位為主）。
func parseSigInputs(sig string) []string {
	sig = strings.TrimSpace(sig)
	open := strings.Index(sig, "(")
	close := strings.LastIndex(sig, ")")
	if open < 0 || close <= open {
		return nil
	}
	inner := sig[open+1 : close]
	if strings.TrimSpace(inner) == "" {
		return []string{}
	}
	// 用逗號切分，但忽略方括號內的逗號（陣列型別不含逗號，安全）
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
