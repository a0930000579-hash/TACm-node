package node

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// ERC-1155（多代幣標準）RPC 閉環：
//   - POST /contract/erc1155/deploy            部署多代幣合約（name/symbol）
//   - GET  /contract/erc1155/{address}         便利查詢 balanceOf/balanceOfBatch/isApprovedForAll/supportsInterface/uri
//   - POST /contract/erc1155/mint              mint(to, id, amount)
//   - POST /contract/erc1155/transfer          safeTransferFrom(from, to, id, amount)
//   - POST /contract/erc1155/batch-transfer    safeBatchTransferFrom(from, to, ids[], amounts[])
//   - POST /contract/erc1155/set-approval-for-all setApprovalForAll(owner, operator, approved)

// handleContractErc1155Deploy POST /contract/erc1155/deploy
// body: {name, symbol}（≤32 bytes 可空）
func (s *RPCServer) handleContractErc1155Deploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Symbol   string `json:"symbol"`
		GasLimit uint64 `json:"gas_limit,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
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
	init := vm.Erc1155InitWithMeta([]byte(req.Name), []byte(req.Symbol))
	memo := vmDeployPrefix + hex.EncodeToString(init)
	if req.GasLimit > 0 {
		memo = vmDeployPrefix + itoa(req.GasLimit) + ":" + hex.EncodeToString(init)
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
		"creator": from, "name": req.Name, "symbol": req.Symbol,
	})
}

// erc1155ReadCall 對合約做只讀模擬呼叫（不寫鏈），回 CallResult。
func (s *RPCServer) erc1155ReadCall(contractTx0, callerTx0 string, calldata []byte) (*vm.CallResult, error) {
	evm, err := tx0To0x(contractTx0)
	if err != nil {
		return nil, err
	}
	caller0x, err := tx0To0x(callerTx0)
	if err != nil {
		return nil, err
	}
	cr, err := s.node.contracts.SimulateCall(evm, caller0x, calldata, nil)
	if err != nil {
		return nil, err
	}
	return cr, nil
}

// handleContractErc1155Info GET /contract/erc1155/{address}
// query: holder=&id=          → balanceOf
//        holder2=&id2=        → balanceOfBatch（兩元素示範）
//        owner=&operator=     → isApprovedForAll
//        iface=               → supportsInterface
//        uri_id=              → uri
func (s *RPCServer) handleContractErc1155Info(w http.ResponseWriter, r *http.Request) {
	contractTx0 := r.PathValue("address")
	if !crypto.IsValidAddress(contractTx0) {
		writeErr(w, http.StatusBadRequest, "invalid contract address")
		return
	}
	evm, err := tx0To0x(contractTx0)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.node.contracts.Get(evm) == nil {
		writeErr(w, http.StatusNotFound, "contract not found")
		return
	}
	q := r.URL.Query()
	resp := map[string]any{
		"ok": true, "address": contractTx0, "evm_address": evm,
		"name":   metaString(s.node.contracts.StorageAt(evm, big.NewInt(1))),
		"symbol": metaString(s.node.contracts.StorageAt(evm, big.NewInt(2))),
	}
	holder, idStr := q.Get("holder"), q.Get("id")
	if holder != "" && idStr != "" {
		if !crypto.IsValidAddress(holder) {
			writeErr(w, http.StatusBadRequest, "invalid holder address")
			return
		}
		id, ok := new(big.Int).SetString(idStr, 10)
		if !ok || id.Sign() < 0 {
			writeErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		holder0x, err := tx0To0x(holder)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cr, err := s.erc1155ReadCall(contractTx0, holder, vm.Erc1155BalanceOfCalldata(holder0x, id))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !cr.OK {
			writeErr(w, http.StatusBadRequest, "balanceOf reverted: "+cr.RevertReason)
			return
		}
		resp["balance_of"] = hexToBigInt(cr.ReturnData).String()
	}
	holder2, id2Str := q.Get("holder2"), q.Get("id2")
	if holder2 != "" && id2Str != "" {
		if !crypto.IsValidAddress(holder2) {
			writeErr(w, http.StatusBadRequest, "invalid holder2 address")
			return
		}
		id2, ok := new(big.Int).SetString(id2Str, 10)
		if !ok || id2.Sign() < 0 {
			writeErr(w, http.StatusBadRequest, "invalid id2")
			return
		}
		holder0x, err := tx0To0x(holder)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		holder20x, err := tx0To0x(holder2)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		id0, ok := new(big.Int).SetString(idStr, 10)
		if !ok || id0.Sign() < 0 {
			writeErr(w, http.StatusBadRequest, "invalid id")
			return
		}
		cr, err := s.erc1155ReadCall(contractTx0, holder,
			vm.Erc1155BalanceOfBatchCalldata([]string{holder0x, holder20x}, []*big.Int{id0, id2}))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !cr.OK {
			writeErr(w, http.StatusBadRequest, "balanceOfBatch reverted: "+cr.RevertReason)
			return
		}
		items, err := abiDecodeUint256Array(cr.ReturnData)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		resp["balance_of_batch"] = items
	}
	owner, operator := q.Get("owner"), q.Get("operator")
	if owner != "" && operator != "" {
		if !crypto.IsValidAddress(owner) || !crypto.IsValidAddress(operator) {
			writeErr(w, http.StatusBadRequest, "invalid owner/operator address")
			return
		}
		owner0x, err := tx0To0x(owner)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		op0x, err := tx0To0x(operator)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cr, err := s.erc1155ReadCall(contractTx0, owner, vm.Erc1155IsApprovedForAllCalldata(owner0x, op0x))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !cr.OK {
			writeErr(w, http.StatusBadRequest, "isApprovedForAll reverted: "+cr.RevertReason)
			return
		}
		resp["is_approved_for_all"] = hexToBigInt(cr.ReturnData).Sign() != 0
	}
	if ifaceStr := q.Get("iface"); ifaceStr != "" {
		iface, err := hexToBytes0x(ifaceStr)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid iface hex (0x… or bare hex)")
			return
		}
		callerTx0, err := s.erc1155DefaultCaller(holder, owner)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		cr, err := s.erc1155ReadCall(contractTx0, callerTx0, vm.Erc1155SupportsInterfaceCalldata(iface))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !cr.OK {
			writeErr(w, http.StatusBadRequest, "supportsInterface reverted: "+cr.RevertReason)
			return
		}
		resp["supports_interface"] = hexToBigInt(cr.ReturnData).Sign() != 0
	}
	if uriIDStr := q.Get("uri_id"); uriIDStr != "" {
		uid, ok := new(big.Int).SetString(uriIDStr, 10)
		if !ok || uid.Sign() < 0 {
			writeErr(w, http.StatusBadRequest, "invalid uri_id")
			return
		}
		callerTx0, err := s.erc1155DefaultCaller(holder, owner)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		cr, err := s.erc1155ReadCall(contractTx0, callerTx0, vm.Erc1155URICalldata(uid))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !cr.OK {
			writeErr(w, http.StatusBadRequest, "uri reverted: "+cr.RevertReason)
			return
		}
		resp["uri"] = cr.ReturnData
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleContractErc1155Mint POST /contract/erc1155/mint
// body: {contract, to, id, amount}
func (s *RPCServer) handleContractErc1155Mint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Contract string `json:"contract"`
		To       string `json:"to"`
		ID       string `json:"id"`
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
	id, amount, err := parseIDAmount(req.ID, req.Amount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to0x, err := tx0To0x(req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.Erc1155MintCalldata(to0x, id, amount)
	txHash, err := s.node.SubmitSignedContractCall(req.Contract, hex.EncodeToString(calldata))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := s.node.keypair.Address()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.Contract,
		"recipient": req.To, "id": id.String(), "amount": amount.String(),
	})
}

// handleContractErc1155Transfer POST /contract/erc1155/transfer
// body: {contract, from, to, id, amount}（safeTransferFrom）
func (s *RPCServer) handleContractErc1155Transfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Contract string `json:"contract"`
		From     string `json:"from"`
		To       string `json:"to"`
		ID       string `json:"id"`
		Amount   string `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !crypto.IsValidAddress(req.Contract) || !crypto.IsValidAddress(req.From) || !crypto.IsValidAddress(req.To) {
		writeErr(w, http.StatusBadRequest, "invalid contract/from/to address")
		return
	}
	id, amount, err := parseIDAmount(req.ID, req.Amount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from0x, err := tx0To0x(req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to0x, err := tx0To0x(req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.Erc1155SafeTransferFromCalldata(from0x, to0x, id, amount)
	txHash, err := s.node.SubmitSignedContractCall(req.Contract, hex.EncodeToString(calldata))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := s.node.keypair.Address()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.Contract,
		"token_from": req.From, "recipient": req.To,
		"id": id.String(), "amount": amount.String(),
	})
}

// handleContractErc1155BatchTransfer POST /contract/erc1155/batch-transfer
// body: {contract, from, to, ids[], amounts[]}
func (s *RPCServer) handleContractErc1155BatchTransfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Contract string   `json:"contract"`
		From     string   `json:"from"`
		To       string   `json:"to"`
		IDs      []string `json:"ids"`
		Amounts  []string `json:"amounts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !crypto.IsValidAddress(req.Contract) || !crypto.IsValidAddress(req.From) || !crypto.IsValidAddress(req.To) {
		writeErr(w, http.StatusBadRequest, "invalid contract/from/to address")
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) != len(req.Amounts) {
		writeErr(w, http.StatusBadRequest, "ids and amounts must be non-empty and equal length")
		return
	}
	ids := make([]*big.Int, 0, len(req.IDs))
	amounts := make([]*big.Int, 0, len(req.Amounts))
	for i, idStr := range req.IDs {
		id, ok := new(big.Int).SetString(idStr, 10)
		if !ok || id.Sign() < 0 {
			writeErr(w, http.StatusBadRequest, "invalid id["+itoa(uint64(i))+"]")
			return
		}
		ids = append(ids, id)
	}
	for i, amtStr := range req.Amounts {
		amt, ok := new(big.Int).SetString(amtStr, 10)
		if !ok || amt.Sign() < 0 {
			writeErr(w, http.StatusBadRequest, "invalid amounts["+itoa(uint64(i))+"]")
			return
		}
		amounts = append(amounts, amt)
	}
	from0x, err := tx0To0x(req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to0x, err := tx0To0x(req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.Erc1155SafeBatchTransferCalldata(from0x, to0x, ids, amounts)
	txHash, err := s.node.SubmitSignedContractCall(req.Contract, hex.EncodeToString(calldata))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := s.node.keypair.Address()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.Contract,
		"token_from": req.From, "recipient": req.To,
		"count": len(ids),
	})
}

// handleContractErc1155SetApprovalForAll POST /contract/erc1155/set-approval-for-all
// body: {contract, owner, operator, approved}
func (s *RPCServer) handleContractErc1155SetApprovalForAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Contract string `json:"contract"`
		Owner    string `json:"owner"`
		Operator string `json:"operator"`
		Approved bool   `json:"approved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if !crypto.IsValidAddress(req.Contract) || !crypto.IsValidAddress(req.Owner) || !crypto.IsValidAddress(req.Operator) {
		writeErr(w, http.StatusBadRequest, "invalid contract/owner/operator address")
		return
	}
	op0x, err := tx0To0x(req.Operator)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	calldata := vm.Erc1155SetApprovalForAllCalldata(op0x, req.Approved)
	txHash, err := s.node.SubmitSignedContractCall(req.Contract, hex.EncodeToString(calldata))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, _ := s.node.keypair.Address()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"from": from, "to": req.Contract,
		"owner": req.Owner, "operator": req.Operator, "approved": req.Approved,
	})
}

// parseIDAmount 解析 id/amount 十進位字串（≥0）。
func parseIDAmount(idStr, amountStr string) (*big.Int, *big.Int, error) {
	id, ok := new(big.Int).SetString(idStr, 10)
	if !ok || id.Sign() < 0 {
		return nil, nil, errInvalidID
	}
	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok || amount.Sign() < 0 {
		return nil, nil, errInvalidAmount
	}
	return id, amount, nil
}

// abiDecodeUint256Array 解析 ABI uint256[]（offset, len, items…）回 []string。
func abiDecodeUint256Array(ret string) ([]string, error) {
	b, err := hexToBytes0x(ret)
	if err != nil {
		return nil, err
	}
	if len(b) < 64 {
		return nil, errBadABI
	}
	n := new(big.Int).SetBytes(b[32:64])
	if n.Sign() < 0 || n.BitLen() > 20 {
		return nil, errBadABI
	}
	cnt := int(n.Uint64())
	out := make([]string, 0, cnt)
	for i := 0; i < cnt; i++ {
		start := 64 + i*32
		if start+32 > len(b) {
			return nil, errBadABI
		}
		out = append(out, new(big.Int).SetBytes(b[start:start+32]).String())
	}
	return out, nil
}

// hexToBigInt 解析「0x…」或裸 hex 回大整數。
func hexToBigInt(hexStr string) *big.Int {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if hexStr == "" {
		return new(big.Int)
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(b)
}

// hexToBytes0x 解析「0x…」或裸 hex 回 bytes。
func hexToBytes0x(hexStr string) ([]byte, error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 != 0 {
		hexStr = "0" + hexStr
	}
	return hex.DecodeString(hexStr)
}

// erc1155DefaultCaller 回查詢 caller：優先 holder/owner，否則節點帳戶（避免零地址）。
func (s *RPCServer) erc1155DefaultCaller(first, second string) (string, error) {
	if first != "" {
		return first, nil
	}
	if second != "" {
		return second, nil
	}
	if s.node.keypair == nil {
		return "", errNoNodeKey
	}
	return s.node.keypair.Address()
}

var (
	errInvalidID     = errString("invalid id")
	errInvalidAmount = errString("invalid amount")
	errBadABI        = errString("malformed ABI return data")
	errNoNodeKey     = errString("node key unavailable")
)

type errString string

func (e errString) Error() string { return string(e) }

// itoa 將 uint64 轉十進位字串。
func itoa(v uint64) string {
	return strconv.FormatUint(v, 10)
}
