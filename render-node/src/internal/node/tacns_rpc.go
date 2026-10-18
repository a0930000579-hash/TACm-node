package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// TAC-ENS 鏈上命名服務 RPC（P1-①）：
//   - POST /tacns/deploy    —— 節點代簽部署 TACNames 註冊表（每鏈部署一次，地址記於記憶體）
//   - GET  /tacns/status    —— 回傳部署狀態與合約地址
//   - POST /tacns/register  —— {label, years, from?} 註冊名稱（from 提供→registerTo 代註冊，
//                               owner=from；from 空→register，owner=節點）
//   - GET  /tacns/query     —— ?label=&addr= 查 owner/expiry/addr/resolve/reverse/nameExists/label
//   - POST /tacns/calldata  —— {op, label, to?, years?, from?} 回傳合約呼叫 calldata（to+data），
//                              供使用者自備私鑰簽名後 POST /tx/submit 上鏈
//
// 命名規則（對齊主流 ENS）：label ≤31 bytes、僅 [a-z0-9-]，顯示為 label.tac。

var (
	tacnsLabelRe   = regexp.MustCompile(`^[a-z0-9-]{1,31}$`)
	tacnsYearSecs  = big.NewInt(31536000)
	tacnsZeroAddr0x = "0x0000000000000000000000000000000000000000"
)

// tacnsAddr 記憶體記錄 TACNames 合約地址（0x 格式）；重啟後需重新部署（鏈上合約不丟失）。
func (s *RPCServer) tacnsContract() (string, error) {
	evm := s.node.tacnsAddr
	if evm == "" {
		return "", fmt.Errorf("tacns: 註冊表尚未部署，請先 POST /tacns/deploy")
	}
	return evm, nil
}

// handleTACNSDeploy POST /tacns/deploy
func (s *RPCServer) handleTACNSDeploy(w http.ResponseWriter, r *http.Request) {
	kp := s.node.keypair
	if kp == nil {
		writeErr(w, http.StatusServiceUnavailable, "node key unavailable")
		return
	}
	from, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nonce := s.node.db.GetNonce(from) + s.node.pendingTxCount(from)
	evmFrom, err := tx0To0x(from)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	addr0x := vm.CreateAddress(evmFrom, uint64(nonce))
	to, err := evmToTx0(addr0x)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	init := vm.TACNamesInit()
	memo := vmDeployPrefix + hex.EncodeToString(init)
	tx := map[string]any{
		"from": from, "to": to,
		"amount": "0", "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
		"memo":   memo,
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tx["signature"] = sig
	txHash, err := s.node.SubmitTransaction(tx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.node.tacnsAddr = addr0x
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"contract_address": to, "evm_address": addr0x, "creator": from,
		"name": "TAC Names Registry", "symbol": "TACNS",
	})
}

// handleTACNSStatus GET /tacns/status
func (s *RPCServer) handleTACNSStatus(w http.ResponseWriter, r *http.Request) {
	evm := s.node.tacnsAddr
	if evm == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployed": false})
		return
	}
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "deployed": true, "address": to, "evm_address": evm,
	})
}

// tacnsLabelHash 驗證 label 並回傳 keccak256(label)。
func tacnsLabelHash(label string) ([]byte, error) {
	label = strings.TrimSpace(strings.ToLower(label))
	label = strings.TrimSuffix(label, ".tac")
	if !tacnsLabelRe.MatchString(label) {
		return nil, fmt.Errorf("tacns: 名稱僅限 [a-z0-9-] 1~31 字元")
	}
	return vm.Keccak256([]byte(label)), nil
}

// handleTACNSRegister POST /tacns/register
// body: {label, years, from?}
func (s *RPCServer) handleTACNSRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Label string `json:"label"`
		Years int64  `json:"years"`
		From  string `json:"from,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	labelHash, err := tacnsLabelHash(req.Label)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Years < 1 || req.Years > 100 {
		writeErr(w, http.StatusBadRequest, "tacns: years 需在 1~100 之間")
		return
	}
	evm, err := s.tacnsContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	kp := s.node.keypair
	if kp == nil {
		writeErr(w, http.StatusServiceUnavailable, "node key unavailable")
		return
	}
	from, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	nonce := s.node.db.GetNonce(from) + s.node.pendingTxCount(from)
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	yearsSec := new(big.Int).Mul(big.NewInt(req.Years), tacnsYearSecs)
	var calldata []byte
	if req.From != "" {
		fromTx0, nerr := s.node.normalizeEthAddr(req.From)
		if nerr != nil {
			writeErr(w, http.StatusBadRequest, nerr.Error())
			return
		}
		owner0x, xerr := tx0To0x(fromTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		calldata = vm.TACNamesRegisterToCalldata(labelHash, owner0x, yearsSec)
	} else {
		calldata = vm.TACNamesRegisterCalldata(labelHash, yearsSec)
	}
	tx := txBodyForCall(from, to, nonce, calldata, kp)
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	tx["signature"] = sig
	txHash, err := s.node.SubmitTransaction(tx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"label": req.Label, "name": req.Label + ".tac",
		"owner": req.From, "years": req.Years,
		"contract_address": to,
	})
}

// txBodyForCall 產生合約呼叫交易的共用欄位（不含簽名）。
func txBodyForCall(from, to string, nonce int64, calldata []byte, kp *crypto.KeyPair) map[string]any {
	pub := ""
	if kp != nil {
		pub = hex.EncodeToString(kp.PublicKeyCompressed())
	}
	return map[string]any{
		"from": from, "to": to,
		"amount": "0", "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": pub,
		"memo":   vmCallPrefix + hex.EncodeToString(calldata),
	}
}

// tacnsSimCall 對 TACNames 合約執行只讀模擬呼叫，回傳 32-byte 結果。
func (s *RPCServer) tacnsSimCall(evm string, caller0x string, calldata []byte) (*big.Int, error) {
	to0x := evm
	caller := caller0x
	if caller == "" {
		caller = tacnsZeroAddr0x
	}
	r, err := s.node.contracts.SimulateCallGas(to0x, caller, calldata, new(big.Int), s.node.contracts.DefaultGas())
	if err != nil {
		return nil, err
	}
	if r.Reverted || !r.OK {
		return nil, fmt.Errorf("tacns: 呼叫被拒 reverted=%v err=%v", r.Reverted, r.Error)
	}
	raw, herr := hex.DecodeString(strings.TrimPrefix(r.ReturnData, "0x"))
	if herr != nil {
		return nil, fmt.Errorf("tacns: 回傳 hex 無效: %w", herr)
	}
	if len(raw) == 0 {
		return new(big.Int), nil
	}
	return new(big.Int).SetBytes(raw), nil
}

// handleTACNSQuery GET /tacns/query?label=&addr=&from=
func (s *RPCServer) handleTACNSQuery(w http.ResponseWriter, r *http.Request) {
	evm, err := s.tacnsContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"ok": true}
	label := r.URL.Query().Get("label")
	addr := r.URL.Query().Get("addr")
	caller := r.URL.Query().Get("from")
	caller0x := ""
	if caller != "" {
		cTx0, cerr := s.node.normalizeEthAddr(caller)
		if cerr != nil {
			writeErr(w, http.StatusBadRequest, cerr.Error())
			return
		}
		caller0x, err = tx0To0x(cTx0)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if label != "" {
		labelHash, lerr := tacnsLabelHash(label)
		if lerr != nil {
			writeErr(w, http.StatusBadRequest, lerr.Error())
			return
		}
		owner, oerr := s.tacnsSimCall(evm, caller0x, vm.TACNamesOwnerCalldata(labelHash))
		if oerr != nil {
			writeErr(w, http.StatusInternalServerError, oerr.Error())
			return
		}
		expiry, eerr := s.tacnsSimCall(evm, caller0x, vm.TACNamesExpiryCalldata(labelHash))
		if eerr != nil {
			writeErr(w, http.StatusInternalServerError, eerr.Error())
			return
		}
		addrVal, aerr := s.tacnsSimCall(evm, caller0x, vm.TACNamesAddrCalldata(labelHash))
		if aerr != nil {
			writeErr(w, http.StatusInternalServerError, aerr.Error())
			return
		}
		exists, xerr := s.tacnsSimCall(evm, caller0x, vm.TACNamesNameExistsCalldata(labelHash))
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		out["label"] = label
		out["name"] = label + ".tac"
		out["name_hash"] = fmt.Sprintf("0x%064x", labelHash)
		out["owner"] = "0x" + fmt.Sprintf("%040x", owner)
		out["expiry"] = expiry.String()
		out["addr"] = "0x" + fmt.Sprintf("%040x", addrVal)
		out["exists"] = exists.Int64() == 1
		// 註：鏈上僅存 labelhash（主流 ENS 同規格），label 字串由應用層維護。
	}
	if addr != "" {
		addrTx0, aerr := s.node.normalizeEthAddr(addr)
		if aerr != nil {
			writeErr(w, http.StatusBadRequest, aerr.Error())
			return
		}
		addr0x, xerr := tx0To0x(addrTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		rev, rerr := s.tacnsSimCall(evm, caller0x, vm.TACNamesReverseCalldata(addr0x))
		if rerr != nil {
			writeErr(w, http.StatusInternalServerError, rerr.Error())
			return
		}
		out["addr"] = addrTx0
		out["reverse_hash"] = fmt.Sprintf("0x%064x", rev)
		// 註：反向解析鏈上僅存 labelhash（主流 ENS 同規格），名稱字串由應用層以 hash 映射。
	}
	if label == "" && addr == "" {
		writeErr(w, http.StatusBadRequest, "tacns: 需提供 label 或 addr 查詢參數")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTACNSCalldata POST /tacns/calldata
// body: {op, label, to?, years?, from?}
// op ∈ setAddr / transfer / renew / setReverse；回傳 to+data 供使用者自備私鑰簽名上鏈。
func (s *RPCServer) handleTACNSCalldata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op    string `json:"op"`
		Label string `json:"label"`
		To    string `json:"to,omitempty"`
		Addr  string `json:"addr,omitempty"`
		Years int64  `json:"years,omitempty"`
		From  string `json:"from,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	evm, err := s.tacnsContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	labelHash, err := tacnsLabelHash(req.Label)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var data []byte
	switch req.Op {
	case "setAddr":
		if req.Addr == "" {
			writeErr(w, http.StatusBadRequest, "tacns: setAddr 需 addr 參數")
			return
		}
		addrTx0, aerr := s.node.normalizeEthAddr(req.Addr)
		if aerr != nil {
			writeErr(w, http.StatusBadRequest, aerr.Error())
			return
		}
		addr0x, xerr := tx0To0x(addrTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		data = vm.TACNamesSetAddrCalldata(labelHash, addr0x)
	case "transfer":
		if req.To == "" {
			writeErr(w, http.StatusBadRequest, "tacns: transfer 需 to 參數")
			return
		}
		toTx0, aerr := s.node.normalizeEthAddr(req.To)
		if aerr != nil {
			writeErr(w, http.StatusBadRequest, aerr.Error())
			return
		}
		to0x, xerr := tx0To0x(toTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		data = vm.TACNamesTransferCalldata(labelHash, to0x)
	case "renew":
		if req.Years < 1 || req.Years > 100 {
			writeErr(w, http.StatusBadRequest, "tacns: years 需在 1~100 之間")
			return
		}
		yearsSec := new(big.Int).Mul(big.NewInt(req.Years), tacnsYearSecs)
		data = vm.TACNamesRenewCalldata(labelHash, yearsSec)
	case "setReverse":
		data = vm.TACNamesSetReverseCalldata(labelHash)
	default:
		writeErr(w, http.StatusBadRequest, "tacns: op 需為 setAddr/transfer/renew/setReverse")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "op": req.Op, "label": req.Label,
		"to": to, "evm_address": evm,
		"data": "0x" + hex.EncodeToString(data),
		"note": "以你的私鑰對 {to, data} 簽名後 POST /tx/submit 上鏈（from=你的地址）",
	})
}
