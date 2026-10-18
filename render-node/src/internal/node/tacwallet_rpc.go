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

// TACWallet 多簽合約錢包 RPC（P1-②，Safe 風格）：
//   - POST /tacwallet/deploy   —— 節點代簽部署 TACWallet（部署者＝節點 keypair，slot10 管理員）
//   - GET  /tacwallet/status   —— 回傳部署狀態、合約地址、ownerCount/threshold/owners 列表
//   - POST /tacwallet/owner    —— {op:"add"|"remove", owner} 節點代簽（部署者 only）
//   - POST /tacwallet/threshold—— {threshold} 節點代簽（部署者 only）
//   - POST /tacwallet/submit   —— {to, value, nonce} 節點代簽 submitTransaction（需呼叫者為 owner）
//   - POST /tacwallet/approve  —— {to, value, nonce, v, r, s} 節點代簽 approveTransaction
//                                （EVM 內 ecrecover 驗證簽名者為 owner；任何人可提交）
//   - GET  /tacwallet/query    —— ?op=ownerCount|threshold|isowner|approvals|isApproved|isValidSig
//   - POST /tacwallet/calldata —— {op,...} 回傳 {to, data}，供使用者自備私鑰簽名後
//                                POST /tx/submit 上鏈（submit/isValidSig 等需自簽場景）
//
// 交易模型：txHash = keccak256(to‖value‖nonce)；approve 附 ECDSA 簽名（v/r/s），
// 合約內驗證簽名者為 owner 且未重複，收集達 threshold 後自動 CALL(to,value) 並 nonce++。
// EIP-1271：isValidSignature(hash,v,r,s) 驗證通過回 magic value 0x1626ba7e。

// walletSimCall 對 TACWallet 合約執行只讀模擬呼叫，回傳 32-byte 結果。
func (s *RPCServer) walletSimCall(evm string, caller0x string, calldata []byte) (*big.Int, error) {
	caller := caller0x
	if caller == "" {
		caller = "0x0000000000000000000000000000000000000000"
	}
	r, err := s.node.contracts.SimulateCallGas(evm, caller, calldata, new(big.Int), s.node.contracts.DefaultGas())
	if err != nil {
		return nil, err
	}
	if r.Reverted || !r.OK {
		return nil, fmt.Errorf("tacwallet: 呼叫被拒 reverted=%v err=%v", r.Reverted, r.Error)
	}
	raw, herr := hex.DecodeString(strings.TrimPrefix(r.ReturnData, "0x"))
	if herr != nil {
		return nil, fmt.Errorf("tacwallet: 回傳 hex 無效: %w", herr)
	}
	if len(raw) == 0 {
		return new(big.Int), nil
	}
	return new(big.Int).SetBytes(raw), nil
}

// walletContract 回傳已部署的 TACWallet 地址（0x 格式）。
func (s *RPCServer) walletContract() (string, error) {
	evm := s.node.tacwalletAddr
	if evm == "" {
		return "", fmt.Errorf("tacwallet: 多簽錢包尚未部署，請先 POST /tacwallet/deploy")
	}
	return evm, nil
}

// walletSubmitTx 共用：節點代簽一筆對 TACWallet 的合約呼叫交易。
func (s *RPCServer) walletSubmitTx(w http.ResponseWriter, evm string, calldata []byte) bool {
	kp := s.node.keypair
	if kp == nil {
		writeErr(w, http.StatusServiceUnavailable, "node key unavailable")
		return false
	}
	from, err := kp.Address()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	nonce := s.node.db.GetNonce(from) + s.node.pendingTxCount(from)
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	tx := txBodyForCall(from, to, nonce, calldata, kp)
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	tx["signature"] = sig
	txHash, err := s.node.SubmitTransaction(tx)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"contract_address": to,
	})
	return true
}

// parseBigWord 解析 0x 前綴 hex 或十進制字串為 big.Int。
func parseBigWord(v string) (*big.Int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, fmt.Errorf("tacwallet: 缺少數值參數")
	}
	if strings.HasPrefix(v, "0x") || strings.HasPrefix(v, "0X") {
		b, err := hex.DecodeString(strings.TrimPrefix(v[2:], "0x"))
		if err != nil {
			return nil, fmt.Errorf("tacwallet: hex 無效 %q", v)
		}
		return new(big.Int).SetBytes(b), nil
	}
	n, ok := new(big.Int).SetString(v, 10)
	if !ok {
		return nil, fmt.Errorf("tacwallet: 數值無效 %q", v)
	}
	return n, nil
}

// handleTACWalletDeploy POST /tacwallet/deploy
func (s *RPCServer) handleTACWalletDeploy(w http.ResponseWriter, r *http.Request) {
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
	init := vm.TACWalletInit()
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
	s.node.tacwalletAddr = addr0x
	s.node.walletOwners = nil
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
		"contract_address": to, "evm_address": addr0x, "creator": from,
		"name": "TAC Multi-Sig Wallet", "standard": "Safe-style N/M + EIP-1271",
	})
}

// handleTACWalletStatus GET /tacwallet/status
func (s *RPCServer) handleTACWalletStatus(w http.ResponseWriter, r *http.Request) {
	evm := s.node.tacwalletAddr
	if evm == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployed": false})
		return
	}
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{"ok": true, "deployed": true, "address": to, "evm_address": evm}
	if c, err := s.walletSimCall(evm, "", vm.TACWalletOwnerCountCalldata()); err == nil {
		out["owner_count"] = c.Uint64()
	}
	if th, err := s.walletSimCall(evm, "", vm.TACWalletThresholdCalldata()); err == nil {
		out["threshold"] = th.Uint64()
	}
	out["owners"] = append([]string{}, s.node.walletOwners...)
	writeJSON(w, http.StatusOK, out)
}

// handleTACWalletOwner POST /tacwallet/owner {op:"add"|"remove", owner}
func (s *RPCServer) handleTACWalletOwner(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op    string `json:"op"`
		Owner string `json:"owner"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	evm, err := s.walletContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	owner0x, nerr := s.node.normalizeEthAddr(req.Owner)
	if nerr != nil {
		writeErr(w, http.StatusBadRequest, nerr.Error())
		return
	}
	owner0x, xerr := tx0To0x(owner0x)
	if xerr != nil {
		writeErr(w, http.StatusInternalServerError, xerr.Error())
		return
	}
	var calldata []byte
	switch req.Op {
	case "add":
		calldata, err = vm.TACWalletAddOwnerCalldata(owner0x)
	case "remove":
		calldata, err = vm.TACWalletRemoveOwnerCalldata(owner0x)
	default:
		writeErr(w, http.StatusBadRequest, "tacwallet: op 需為 add|remove")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.walletSubmitTx(w, evm, calldata) {
		return
	}
	// 記憶體記錄 owner 列表（供面板展示；重啟後以鏈上 ownerCount/isOwner 為準）
	if req.Op == "add" {
		s.node.walletOwners = appendUnique(s.node.walletOwners, owner0x)
	} else {
		s.node.walletOwners = removeString(s.node.walletOwners, owner0x)
	}
}

// handleTACWalletThreshold POST /tacwallet/threshold {threshold}
func (s *RPCServer) handleTACWalletThreshold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Threshold int64 `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	if req.Threshold < 1 {
		writeErr(w, http.StatusBadRequest, "tacwallet: threshold 需 ≥ 1")
		return
	}
	evm, err := s.walletContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.walletSubmitTx(w, evm, vm.TACWalletChangeThresholdCalldata(req.Threshold)) {
		return
	}
}

// handleTACWalletSubmit POST /tacwallet/submit {to, value, nonce}
// 節點代簽 submitTransaction（EVM 檢查 caller 為 owner；部署者可先 addOwner 自己）。
func (s *RPCServer) handleTACWalletSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To    string `json:"to"`
		Value string `json:"value"`
		Nonce string `json:"nonce,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	evm, err := s.walletContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	toTx0, nerr := s.node.normalizeEthAddr(req.To)
	if nerr != nil {
		writeErr(w, http.StatusBadRequest, nerr.Error())
		return
	}
	to0x, xerr := tx0To0x(toTx0)
	if xerr != nil {
		writeErr(w, http.StatusInternalServerError, xerr.Error())
		return
	}
	toVal, perr := parseBigWord(req.Value)
	if perr != nil {
		writeErr(w, http.StatusBadRequest, perr.Error())
		return
	}
	nonce := new(big.Int)
	if req.Nonce != "" {
		nonce, perr = parseBigWord(req.Nonce)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
	}
	if !s.walletSubmitTx(w, evm, vm.TACWalletSubmitCalldata(toWord(to0x), toVal, nonce)) {
		return
	}
}

// handleTACWalletApprove POST /tacwallet/approve {to, value, nonce, v, r, s}
// 節點代簽 approveTransaction；EVM 內 ecrecover 驗證 v/r/s 簽名者為 owner。
func (s *RPCServer) handleTACWalletApprove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To    string `json:"to"`
		Value string `json:"value"`
		Nonce string `json:"nonce"`
		V     int64  `json:"v"`
		R     string `json:"r"`
		S     string `json:"s"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	evm, err := s.walletContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	toTx0, nerr := s.node.normalizeEthAddr(req.To)
	if nerr != nil {
		writeErr(w, http.StatusBadRequest, nerr.Error())
		return
	}
	to0x, xerr := tx0To0x(toTx0)
	if xerr != nil {
		writeErr(w, http.StatusInternalServerError, xerr.Error())
		return
	}
	toVal, perr := parseBigWord(req.Value)
	if perr != nil {
		writeErr(w, http.StatusBadRequest, perr.Error())
		return
	}
	nonce, perr := parseBigWord(req.Nonce)
	if perr != nil {
		writeErr(w, http.StatusBadRequest, perr.Error())
		return
	}
	rVal, perr := parseBigWord(req.R)
	if perr != nil {
		writeErr(w, http.StatusBadRequest, perr.Error())
		return
	}
	sVal, perr := parseBigWord(req.S)
	if perr != nil {
		writeErr(w, http.StatusBadRequest, perr.Error())
		return
	}
	if req.V < 27 || req.V > 28 {
		writeErr(w, http.StatusBadRequest, "tacwallet: v 需為 27|28")
		return
	}
	if !s.walletSubmitTx(w, evm, vm.TACWalletApproveCalldata(toWord(to0x), toVal, nonce, big.NewInt(req.V), rVal, sVal)) {
		return
	}
}

// handleTACWalletQuery GET /tacwallet/query?op=&owner=&to=&value=&nonce=&signer=&hash=&v=&r=&s=
func (s *RPCServer) handleTACWalletQuery(w http.ResponseWriter, r *http.Request) {
	evm, err := s.walletContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	op := r.URL.Query().Get("op")
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
	var out any
	switch op {
	case "ownerCount":
		var c *big.Int
		c, err = s.walletSimCall(evm, caller0x, vm.TACWalletOwnerCountCalldata())
		out = map[string]any{"owner_count": c.Uint64()}
	case "threshold":
		var th *big.Int
		th, err = s.walletSimCall(evm, caller0x, vm.TACWalletThresholdCalldata())
		out = map[string]any{"threshold": th.Uint64()}
	case "isowner":
		owner := r.URL.Query().Get("owner")
		if owner == "" {
			writeErr(w, http.StatusBadRequest, "tacwallet: 需 owner 參數")
			return
		}
		oTx0, cerr := s.node.normalizeEthAddr(owner)
		if cerr != nil {
			writeErr(w, http.StatusBadRequest, cerr.Error())
			return
		}
		o0x, xerr := tx0To0x(oTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		var cd []byte
		cd, err = vm.TACWalletIsOwnerCalldata(o0x)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var v *big.Int
		v, err = s.walletSimCall(evm, caller0x, cd)
		out = map[string]any{"is_owner": v.Sign() != 0, "owner": o0x}
	case "approvals":
		to, val, nonce, perr := walletTxParams(r)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		var v *big.Int
		v, err = s.walletSimCall(evm, caller0x, vm.TACWalletApprovalsCalldata(to, val, nonce))
		out = map[string]any{"approvals": v.Uint64()}
	case "isApproved":
		to, val, nonce, perr := walletTxParams(r)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		signer := r.URL.Query().Get("signer")
		if signer == "" {
			writeErr(w, http.StatusBadRequest, "tacwallet: 需 signer 參數")
			return
		}
		sTx0, cerr := s.node.normalizeEthAddr(signer)
		if cerr != nil {
			writeErr(w, http.StatusBadRequest, cerr.Error())
			return
		}
		s0x, xerr := tx0To0x(sTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		var cd []byte
		cd, err = vm.TACWalletIsApprovedCalldata(to, val, nonce, s0x)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var v *big.Int
		v, err = s.walletSimCall(evm, caller0x, cd)
		out = map[string]any{"is_approved": v.Sign() != 0, "signer": s0x}
	case "isValidSig":
		hash := r.URL.Query().Get("hash")
		if len(hash) != 66 || !strings.HasPrefix(hash, "0x") {
			writeErr(w, http.StatusBadRequest, "tacwallet: hash 需為 0x + 64 hex")
			return
		}
		hb, herr := hex.DecodeString(hash[2:])
		if herr != nil || len(hb) != 32 {
			writeErr(w, http.StatusBadRequest, "tacwallet: hash 需為 32 bytes")
			return
		}
		v, perr := parseBigWord(r.URL.Query().Get("v"))
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		rv, perr := parseBigWord(r.URL.Query().Get("r"))
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		sv, perr := parseBigWord(r.URL.Query().Get("s"))
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		var res *big.Int
		res, err = s.walletSimCall(evm, caller0x, vm.TACWalletIsValidSigCalldata(hb, v, rv, sv))
		magic := len(res.Bytes()) >= 4 && res.Bytes()[0] == 0x16 && res.Bytes()[1] == 0x26 &&
			res.Bytes()[2] == 0xba && res.Bytes()[3] == 0x7e
		out = map[string]any{"valid": magic, "magic": fmt.Sprintf("0x%x", res)}
	default:
		writeErr(w, http.StatusBadRequest, "tacwallet: 未知 op，支援 ownerCount|threshold|isowner|approvals|isApproved|isValidSig")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": out})
}

// walletTxParams 從 query 讀 to/value/nonce（0x 或十進制）。
func walletTxParams(r *http.Request) (*big.Int, *big.Int, *big.Int, error) {
	to := r.URL.Query().Get("to")
	val := r.URL.Query().Get("value")
	nonce := r.URL.Query().Get("nonce")
	toVal, err := parseBigWord(to)
	if err != nil {
		return nil, nil, nil, err
	}
	valVal, err := parseBigWord(val)
	if err != nil {
		return nil, nil, nil, err
	}
	nonceVal, err := parseBigWord(nonce)
	if err != nil {
		return nil, nil, nil, err
	}
	return toVal, valVal, nonceVal, nil
}

// handleTACWalletCalldata POST /tacwallet/calldata {op, to?, value?, nonce?, owner?, hash?, v?, r?, s?}
// 回傳 {to, data} 供使用者自備私鑰簽名後 POST /tx/submit 上鏈。
func (s *RPCServer) handleTACWalletCalldata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Op    string `json:"op"`
		To    string `json:"to,omitempty"`
		Value string `json:"value,omitempty"`
		Nonce string `json:"nonce,omitempty"`
		Owner string `json:"owner,omitempty"`
		Hash  string `json:"hash,omitempty"`
		V     string `json:"v,omitempty"`
		R     string `json:"r,omitempty"`
		S     string `json:"s,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	evm, err := s.walletContract()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := evmToTx0(evm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var calldata []byte
	switch req.Op {
	case "submit":
		if req.To == "" || req.Value == "" {
			writeErr(w, http.StatusBadRequest, "tacwallet: submit 需 to/value")
			return
		}
		toTx0, nerr := s.node.normalizeEthAddr(req.To)
		if nerr != nil {
			writeErr(w, http.StatusBadRequest, nerr.Error())
			return
		}
		to0x, xerr := tx0To0x(toTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		toVal, perr := parseBigWord(req.Value)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		nonce := new(big.Int)
		if req.Nonce != "" {
			nonce, perr = parseBigWord(req.Nonce)
			if perr != nil {
				writeErr(w, http.StatusBadRequest, perr.Error())
				return
			}
		}
		calldata = vm.TACWalletSubmitCalldata(toWord(to0x), toVal, nonce)
	case "approve":
		if req.To == "" || req.Value == "" || req.Nonce == "" || req.R == "" || req.S == "" {
			writeErr(w, http.StatusBadRequest, "tacwallet: approve 需 to/value/nonce/r/s")
			return
		}
		toTx0, nerr := s.node.normalizeEthAddr(req.To)
		if nerr != nil {
			writeErr(w, http.StatusBadRequest, nerr.Error())
			return
		}
		to0x, xerr := tx0To0x(toTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		toVal, perr := parseBigWord(req.Value)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		nonce, perr := parseBigWord(req.Nonce)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		vVal, perr := parseBigWord(req.V)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		rVal, perr := parseBigWord(req.R)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		sVal, perr := parseBigWord(req.S)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		calldata = vm.TACWalletApproveCalldata(toWord(to0x), toVal, nonce, vVal, rVal, sVal)
	case "isValidSig":
		if req.Hash == "" || req.R == "" || req.S == "" {
			writeErr(w, http.StatusBadRequest, "tacwallet: isValidSig 需 hash/r/s")
			return
		}
		hb, herr := hex.DecodeString(strings.TrimPrefix(req.Hash, "0x"))
		if herr != nil || len(hb) != 32 {
			writeErr(w, http.StatusBadRequest, "tacwallet: hash 需為 32 bytes hex")
			return
		}
		vVal, perr := parseBigWord(req.V)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		rVal, perr := parseBigWord(req.R)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		sVal, perr := parseBigWord(req.S)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		calldata = vm.TACWalletIsValidSigCalldata(hb, vVal, rVal, sVal)
	case "addowner", "removeowner":
		if req.Owner == "" {
			writeErr(w, http.StatusBadRequest, "tacwallet: 需 owner 參數")
			return
		}
		oTx0, nerr := s.node.normalizeEthAddr(req.Owner)
		if nerr != nil {
			writeErr(w, http.StatusBadRequest, nerr.Error())
			return
		}
		o0x, xerr := tx0To0x(oTx0)
		if xerr != nil {
			writeErr(w, http.StatusInternalServerError, xerr.Error())
			return
		}
		if req.Op == "addowner" {
			calldata, err = vm.TACWalletAddOwnerCalldata(o0x)
		} else {
			calldata, err = vm.TACWalletRemoveOwnerCalldata(o0x)
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "tacwallet: 未知 op，支援 submit|approve|isValidSig|addowner|removeowner")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "to": to, "data": "0x" + hex.EncodeToString(calldata),
	})
}

// appendUnique / removeString：owner 列表記憶體輔助。
func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func removeString(list []string, v string) []string {
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// toWord 把 0x 20-byte 地址轉為 32-byte word（與合約內 ecrecover signer 一致）。
func toWord(addr0x string) *big.Int {
	b, err := hex.DecodeString(strings.TrimPrefix(addr0x, "0x"))
	if err != nil || len(b) != 20 {
		return new(big.Int)
	}
	return new(big.Int).SetBytes(b)
}
