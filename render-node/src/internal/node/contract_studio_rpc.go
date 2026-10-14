package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// 本檔提供「代幣發行閉環」RPC 端點（M44）：
//   - POST /contract/deploy          —— 節點官方金鑰代簽發行標準代幣（ERC-20 風格）
//   - GET  /contract/call/{address}  —— 只讀模擬呼叫（totalSupply/balanceOf/name/symbol）
//   - POST /contract/call            —— 節點官方金鑰代簽鏈上呼叫（transfer 等）
//
// 一般用戶若持有自己的私鑰，仍可直接 POST /tx/submit 走真實 ECDSA 管道；
// 此處的「節點代簽」是官方/演示發行入口（from=節點金鑰地址），前端無需私鑰。

// metaString 把 32-byte 右對齊 storage 值解回可讀字串（去 leading 0，全 0 為空）。
func metaString(v *big.Int) string {
	if v == nil || v.Sign() == 0 {
		return ""
	}
	b := v.Bytes()
	// 僅在可安全解為 UTF-8 時回傳字串，否則保留 hex 供前端顯示。
	if len(b) == 0 {
		return ""
	}
	return string(b)
}

// handleContractDeploy POST /contract/deploy
// body: {name, symbol, supply}（supply 必填，1..1e12；name/symbol ≤32 bytes 可空）
func (s *RPCServer) handleContractDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Symbol   string `json:"symbol"`
		Supply   string `json:"supply"`
		GasLimit uint64 `json:"gas_limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	supply, ok := new(big.Int).SetString(req.Supply, 10)
	if !ok || supply.Sign() <= 0 || supply.Cmp(big.NewInt(1_000_000_000_000)) > 0 {
		writeErr(w, http.StatusBadRequest, "invalid supply (must be 1..1000000000000)")
		return
	}
	if len(req.Name) > 32 || len(req.Symbol) > 32 {
		writeErr(w, http.StatusBadRequest, "name/symbol must be ≤32 bytes")
		return
	}
	if req.GasLimit > vm.NewContext().Gas {
		writeErr(w, http.StatusBadRequest, "gas_limit exceeds node maximum (10000000)")
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
	var init []byte
	if req.Name != "" || req.Symbol != "" {
		init = vm.Erc20InitWithMeta(supply, []byte(req.Name), []byte(req.Symbol))
	} else {
		init = vm.Erc20Init(supply)
	}
	memo := vmDeployPrefix + hex.EncodeToString(init)
	if req.GasLimit > 0 {
		memo = fmt.Sprintf("%s%d:%s", vmDeployPrefix, req.GasLimit, hex.EncodeToString(init))
	}
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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"contract_address": to, "evm_address": addr0x,
		"creator": from, "supply": supply.String(),
		"name": req.Name, "symbol": req.Symbol,
	})
}

// handleContractCallQuery GET /contract/call/{address}?from=&calldata=<hex>
// 只讀模擬呼叫（不寫鏈）：用於 totalSupply/balanceOf/name/symbol 等查詢。
func (s *RPCServer) handleContractCallQuery(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	evm, err := tx0To0x(addr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cdHex := r.URL.Query().Get("calldata")
	calldata, err := hexInput(cdHex)
	if err != nil || len(calldata) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid calldata hex")
		return
	}
	caller := r.URL.Query().Get("from")
	caller0x := vm.ZeroAddress
	if caller != "" {
		c0x, err := tx0To0x(caller)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid from address")
			return
		}
		caller0x = c0x
	}
	res, err := s.node.contracts.SimulateCall(evm, caller0x, calldata, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          res.OK,
		"return_data": res.ReturnData,
		"gas_used":    res.GasUsed,
		"error":       res.Error,
		"reverted":    res.Reverted,
	})
}

// handleContractCallSubmit POST /contract/call
// body: {to, calldata}（節點金鑰代簽鏈上呼叫；to 為合約地址，calldata 為 hex）
func (s *RPCServer) handleContractCallSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To       string `json:"to"`
		Calldata string `json:"calldata"`
		GasLimit uint64 `json:"gas_limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !crypto.IsValidAddress(req.To) {
		writeErr(w, http.StatusBadRequest, "invalid contract address")
		return
	}
	calldata, err := hexInput(req.Calldata)
	if err != nil || len(calldata) == 0 {
		writeErr(w, http.StatusBadRequest, "invalid calldata hex")
		return
	}
	if req.GasLimit > vm.NewContext().Gas {
		writeErr(w, http.StatusBadRequest, "gas_limit exceeds node maximum (10000000)")
		return
	}
	txHash, err := s.node.SubmitSignedContractCallGas(req.To, req.Calldata, req.GasLimit)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := s.node.keypair.Address()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.To,
	})
}

// holderKey 把 tx0/0x 地址轉為 storage key（與 VM addrToInt 一致：hex 解碼）。
func holderKey(addr string) (*big.Int, error) {
	s := strings.TrimPrefix(addr, "0x")
	if strings.HasPrefix(addr, "tx0") {
		evm, err := tx0To0x(addr)
		if err != nil {
			return nil, err
		}
		s = strings.TrimPrefix(evm, "0x")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// handleContractERC20Info GET /contract/erc20/{address}?holder=tx0...
// 標準代幣便利查詢：回傳 totalSupply/name/symbol 與（可選）指定持有人餘額，
// 前端無需自行組裝 calldata。
func (s *RPCServer) handleContractERC20Info(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	evm, err := tx0To0x(addr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.contracts.Get(evm) == nil {
		writeErr(w, http.StatusNotFound, "contract not found")
		return
	}
	supply := s.node.contracts.StorageAt(evm, big.NewInt(0))
	name := metaString(s.node.contracts.StorageAt(evm, big.NewInt(1)))
	symbol := metaString(s.node.contracts.StorageAt(evm, big.NewInt(2)))
	out := map[string]any{
		"ok": true, "address": addr, "evm_address": evm,
		"supply": supply.String(), "name": name, "symbol": symbol,
		"balance": "0",
	}
	if holder := r.URL.Query().Get("holder"); holder != "" {
		k, err := holderKey(holder)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid holder address")
			return
		}
		out["balance"] = s.node.contracts.StorageAt(evm, k).String()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleContractERC20Transfer POST /contract/erc20/transfer
// body: {contract, to, amount}——伺服器組 transfer calldata（to 轉 0x）並以
// 節點金鑰代簽上鏈（官方帳戶轉帳，前端無需私鑰）。
func (s *RPCServer) handleContractERC20Transfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Contract string `json:"contract"`
		To       string `json:"to"`
		Amount   string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !crypto.IsValidAddress(req.Contract) || !crypto.IsValidAddress(req.To) {
		writeErr(w, http.StatusBadRequest, "invalid contract/to address")
		return
	}
	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid amount")
		return
	}
	to0x, err := tx0To0x(req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.Erc20TransferCalldata(to0x, amount)
	txHash, err := s.node.SubmitSignedContractCall(req.Contract, hex.EncodeToString(calldata))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := s.node.keypair.Address()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.Contract,
		"recipient": req.To, "amount": req.Amount,
	})
}
