package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"

	"github.com/btcsuite/btcd/btcec/v2"
)

// ethRPCRequest/Response 為 JSON-RPC 2.0 外殼（eth_* 相容層）。
type ethRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type ethRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// handleEthRPC 提供 Ethereum 生態相容的 JSON-RPC 端點（POST /eth）。
// 讓 MetaMask、Truffle、Hardhat 等工具以標準 eth_* 介面讀寫 TAC 鏈。
func (s *RPCServer) handleEthRPC(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeEthErr(w, nil, -32600, "method must be POST")
		return
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeEthErr(w, nil, -32700, "parse error")
		return
	}
	trimmed := bytes.TrimSpace(raw)
	// M117：JSON-RPC 2.0 batch（陣列請求）——主流鏈 RPC 標準。
	if len(trimmed) > 0 && trimmed[0] == '[' {
		s.handleEthBatch(w, trimmed)
		return
	}
	var req ethRPCRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeEthErr(w, nil, -32700, "parse error")
		return
	}
	resp := s.dispatchEthRPC(req)
	_ = json.NewEncoder(w).Encode(resp)
}

// dispatchEthRPC 分派單一 eth_* 請求，回傳完整 JSON-RPC 2.0 回應。
// 單一與 batch 兩路徑共用，確保行為一致。
func (s *RPCServer) dispatchEthRPC(req ethRPCRequest) ethRPCResponse {
	if req.JSONRPC != "2.0" {
		return ethRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: ethError(-32600, "jsonrpc must be 2.0")}
	}

	var result any
	var rpcErr *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	switch req.Method {
	case "web3_clientVersion":
		result = "tacm-go/1.0"
	case "net_version", "eth_chainId":
		result = "0x" + strconv.FormatInt(crypto.EthChainID, 16)
	case "net_listening":
		result = true // M133-A：節點恆監聽，主流錢包握手檢查相容
	case "net_peerCount":
		out, err := s.netPeerCount()
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "personal_sign":
		out, err := s.ethPersonalSign(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_sign":
		out, err := s.ethSign(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_signTypedData_v4":
		out, err := s.ethSignTypedDataV4(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_ecRecover":
		out, err := s.ethEcRecover(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "personal_ecRecover":
		out, err := s.personalEcRecover(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_blobBaseFee":
		result = "0x0" // 本鏈無 blob 支援，回 0（EIP-4844 相容）
	case "web3_sha3":
		out, err := s.web3Sha3(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_createAccessList": // EIP-2930：主流錢包（viem/ethers）發送前會呼叫
		out, err := s.ethCreateAccessList(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_mining":
		result = false // 本鏈挖礦由礦機經 /api/miner/* 回報，節點本身不挖
	case "eth_hashrate":
		result = "0x0"
	case "eth_submitWork":
		result = false // 不接收外部 PoW work
	case "eth_submitHashrate":
		result = false
	case "eth_blockNumber":
		b, err := s.node.db.GetLatestBlock()
		if err != nil {
			rpcErr = ethError(-32603, fmt.Sprintf("block query: %v", err))
		} else if b == nil {
			result = "0x0"
		} else {
			result = hexBig(b.Height)
		}
	case "eth_gasPrice":
		out, err := s.ethGasPrice()
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getBalance":
		out, err := s.ethGetBalance(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getTransactionCount":
		out, err := s.ethGetTransactionCount(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getCode":
		out, err := s.ethGetCode(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_call":
		out, err := s.ethCall(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getBlockByNumber":
		out, err := s.ethGetBlockByNumber(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_sendRawTransaction":
		out, err := s.node.SubmitEthRawTx(paramString(req.Params, 0))
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getTransactionByHash":
		out, err := s.ethGetTransactionByHash(req.Params)
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getTransactionReceipt":
		out, err := s.ethGetTransactionReceipt(req.Params)
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getBlockReceipts":
		out, err := s.ethGetBlockReceipts(req.Params)
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getBlockByHash":
		out, err := s.ethGetBlockByHash(req.Params)
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getStorageAt":
		out, err := s.ethGetStorageAt(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_estimateGas":
		out, err := s.ethEstimateGas(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getLogs":
		out, err := s.ethGetLogs(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_newFilter":
		out, err := s.ethNewFilter(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_newBlockFilter":
		out, err := s.ethNewBlockFilter()
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_newPendingTransactionFilter":
		out, err := s.ethNewPendingTransactionFilter()
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getFilterChanges":
		out, err := s.ethGetFilterChanges(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getFilterLogs":
		out, err := s.ethGetFilterLogs(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_uninstallFilter":
		out, err := s.ethUninstallFilter(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_feeHistory":
		out, err := s.ethFeeHistory(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getBlockTransactionCountByNumber":
		out, err := s.ethGetBlockTransactionCountByNumber(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getBlockTransactionCountByHash":
		out, err := s.ethGetBlockTransactionCountByHash(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getTransactionByBlockNumberAndIndex":
		out, err := s.ethGetTransactionByBlockNumberAndIndex(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getTransactionByBlockHashAndIndex":
		out, err := s.ethGetTransactionByBlockHashAndIndex(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getUncleCountByBlockNumber":
		out, err := s.ethGetUncleCountByBlockNumber(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_getUncleCountByBlockHash":
		out, err := s.ethGetUncleCountByBlockHash(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "eth_syncing":
		result = s.ethSyncing()
	case "eth_protocolVersion":
		result = s.ethProtocolVersion()
	case "eth_coinbase":
		out, err := s.ethCoinbase()
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_accounts":
		result = s.ethAccounts()
	case "eth_requestAccounts":
		// P0-③ MetaMask/Web3 直連：EIP-1193 request({method:'eth_requestAccounts'})。
		// 節點為無狀態金鑰管理——回報節點管理的帳戶（與 eth_accounts 同源）。
		result = s.ethAccounts()
	case "eth_maxPriorityFeePerGas":
		result = s.ethMaxPriorityFeePerGas()
	case "eth_getUncleByBlockHashAndIndex":
		result = s.ethUncleByHashAndIndex(req.Params)
	case "eth_getUncleByBlockNumberAndIndex":
		result = s.ethUncleByNumberAndIndex(req.Params)
	case "txpool_status":
		result = s.txpoolStatus()
	case "txpool_content":
		out, err := s.txpoolContent()
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "eth_getProof":
		out, err := s.ethGetProof(req.Params)
		if err != nil {
			rpcErr = ethError(-32602, err.Error())
		} else {
			result = out
		}
	case "debug_traceTransaction":
		out, err := s.debugTraceTransaction(req.Params)
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	case "debug_traceCall": // M151e：主流調試工具（hardhat/viem/Remix）模擬呼叫追蹤
		out, err := s.debugTraceCall(req.Params)
		if err != nil {
			rpcErr = ethError(-32603, err.Error())
		} else {
			result = out
		}
	default:
		rpcErr = ethError(-32601, "method not found: "+req.Method)
	}

	return ethRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr}
}

// handleEthBatch 處理 JSON-RPC 2.0 陣列請求（M117）：以 Goroutine 並行分派各筆，
// 依輸入順序回傳結果陣列；單筆解析失敗僅影響該筆（-32700）。
func (s *RPCServer) handleEthBatch(w http.ResponseWriter, raw []byte) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		writeEthErr(w, nil, -32700, "parse error")
		return
	}
	if len(items) == 0 {
		// 空陣列：JSON-RPC 2.0 規範回單一錯誤物件。
		writeEthErr(w, nil, -32600, "invalid request")
		return
	}
	responses := make([]ethRPCResponse, len(items))
	// 並行分派（每筆獨立、無共享寫入；eth_* 讀寫內部自帶鎖）。
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Add(1)
		go func(i int, it json.RawMessage) {
			defer wg.Done()
			var req ethRPCRequest
			if err := json.Unmarshal(it, &req); err != nil {
				responses[i] = ethRPCResponse{JSONRPC: "2.0", ID: nil, Error: ethError(-32700, "parse error")}
				return
			}
			responses[i] = s.dispatchEthRPC(req)
		}(i, it)
	}
	wg.Wait()
	_ = json.NewEncoder(w).Encode(responses)
}

func writeEthErr(w http.ResponseWriter, id any, code int, msg string) {
	writeEthResult(w, id, nil, ethError(code, msg))
}

func ethError(code int, msg string) *struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
} {
	return &struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: msg}
}

func writeEthResult(w http.ResponseWriter, id any, result any, rpcErr *struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}) {
	resp := ethRPCResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr}
	_ = json.NewEncoder(w).Encode(resp)
}

// paramString 取 params[i] 字串；缺失回 ""。
func paramString(params []any, i int) string {
	if i >= len(params) {
		return ""
	}
	if s, ok := params[i].(string); ok {
		return s
	}
	return ""
}

// hexBig 把 int64 轉為 0x hex（eth 慣例，無前導零）。
func hexBig(v int64) string {
	if v == 0 {
		return "0x0"
	}
	return "0x" + strconv.FormatInt(v, 16)
}

// normalizeEthAddr 接受 0x 或 tx0 地址，回 tx0 格式。
func (n *Node) normalizeEthAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if crypto.IsValidAddress(addr) {
		return addr, nil
	}
	s := strings.TrimPrefix(addr, "0x")
	b, err := hex.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("eth: 地址 hex 錯誤: %w", err)
	}
	if len(b) != 20 {
		return "", fmt.Errorf("eth: 地址應為20字節, got %d", len(b))
	}
	return crypto.Hash160ToAddress(b), nil
}

func (s *RPCServer) ethGetBalance(params []any) (string, error) {
	addr, err := s.node.normalizeEthAddr(paramString(params, 0))
	if err != nil {
		return "", err
	}
	bal := s.node.db.GetBalance(addr)
	// 鏈上餘額為 TACm 十進位（可能含小數）；eth 側為 wei（×1e18）。
	rat, ok := new(big.Rat).SetString(bal)
	if !ok {
		rat = new(big.Rat)
	}
	num := new(big.Int).Mul(rat.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	wei := new(big.Int).Quo(num, rat.Denom())
	return "0x" + wei.Text(16), nil
}

func (s *RPCServer) ethGetTransactionCount(params []any) (string, error) {
	addr, err := s.node.normalizeEthAddr(paramString(params, 0))
	if err != nil {
		return "", err
	}
	return hexBig(s.node.db.GetNonce(addr) + s.node.pendingTxCount(addr)), nil
}

func (s *RPCServer) ethGetCode(params []any) (string, error) {
	addr, err := s.node.normalizeEthAddr(paramString(params, 0))
	if err != nil {
		return "", err
	}
	addr0x, err := tx0To0x(addr)
	if err != nil {
		return "", err
	}
	code := s.node.contracts.GetCode(addr0x)
	if len(code) == 0 {
		return "0x", nil
	}
	return "0x" + hex.EncodeToString(code), nil
}

// ethCall 執行只讀模擬呼叫（不寫鏈）。
func (s *RPCServer) ethCall(params []any) (string, error) {
	callObj, ok := params[0].(map[string]any)
	if !ok {
		return "", errors.New("eth: params[0] 需為 call object")
	}
	to := getStringFieldAny(callObj, "to")
	from := getStringFieldAny(callObj, "from")
	data := getStringFieldAny(callObj, "data")
	value := getStringFieldAny(callObj, "value")

	toTx0, err := s.node.normalizeEthAddr(to)
	if err != nil {
		return "", err
	}
	to0x, err := tx0To0x(toTx0)
	if err != nil {
		return "", err
	}
	caller0x := "0x0000000000000000000000000000000000000000"
	if from != "" {
		fTx0, ferr := s.node.normalizeEthAddr(from)
		if ferr != nil {
			return "", ferr
		}
		caller0x, err = tx0To0x(fTx0)
		if err != nil {
			return "", err
		}
	}
	calldata, err := hexInput(data)
	if err != nil {
		return "", fmt.Errorf("eth: data hex 錯誤: %w", err)
	}
	callValue := new(big.Int)
	if value != "" {
		callValue, _ = new(big.Int).SetString(strings.TrimPrefix(value, "0x"), 16)
		if callValue == nil {
			return "", errors.New("eth: value 解析失敗")
		}
	}
	r, err := s.node.contracts.SimulateCallGas(to0x, caller0x, calldata, callValue, s.node.contracts.DefaultGas())
	if err != nil {
		return "", err
	}
	if !r.OK || r.Reverted {
		return "", errors.New("eth: call reverted: " + r.RevertReason)
	}
	return "0x" + strings.TrimPrefix(r.ReturnData, "0x"), nil
}

// ethGetBlockByNumber 回區塊資訊；第二參數 true 時回完整交易物件（MetaMask 慣例）。
func (s *RPCServer) ethGetBlockByNumber(params []any) (any, error) {
	tag := paramString(params, 0)
	fullTx := paramBool(params, 1)
	b, err := s.node.db.GetLatestBlock()
	if err != nil || b == nil {
		return nil, errors.New("eth: 無區塊")
	}
	if tag != "latest" && tag != "" && tag != "pending" {
		if h := parseEthBlock(tag, b.Height); h >= 0 {
			if bb, berr := s.node.db.GetBlock(h); berr == nil && bb != nil {
				b = bb
			}
		}
	}
	return s.ethBlockObject(b, fullTx)
}

// paramBool 取 params[i] 布林；缺失回 false。
func paramBool(params []any, i int) bool {
	if i >= len(params) {
		return false
	}
	if b, ok := params[i].(bool); ok {
		return b
	}
	return false
}

// ethBlockObject 以太標準區塊 JSON；transactions 依 fullTx 回完整物件或 hash 列表。
func (s *RPCServer) ethBlockObject(b *chaindb.Block, fullTx bool) (map[string]any, error) {
	txs, err := s.node.db.GetTransactionsByBlock(b.Height)
	if err != nil {
		return nil, err
	}
	txArr := make([]any, 0, len(txs))
	if fullTx {
		for i := range txs {
			txArr = append(txArr, s.ethTxObject(txs[i]))
		}
	} else {
		for i := range txs {
			txArr = append(txArr, "0x"+txs[i].TxHash)
		}
	}
	miner := "0x"
	if b.ProposerAddress != "" {
		if m, err := tx0To0x(b.ProposerAddress); err == nil {
			miner = m
		}
	}
	gasLimit := b.GasLimit
	if gasLimit <= 0 {
		gasLimit = chaindb.BlockGasLimit
	}
	baseFee := "0x0"
	if b.BaseFee != "" {
		if bf, err := strconv.ParseFloat(b.BaseFee, 64); err == nil && bf > 0 {
			baseFee = feeToWeiHex(bf)
		}
	}
	diff := int64(b.Difficulty)
	if diff <= 0 {
		diff = 1 // 無難度鏈回 1，避免解析器誤判為 0
	}
	// M130-A：主流區塊物件標準欄位（ethers/block explorer 解析相容）。
	// TACm 無 trie/bloom——stateRoot/logsBloom 回零值並註明語意。
	return map[string]any{
		"number":           hexBig(b.Height),
		"hash":             "0x" + b.Hash,
		"parentHash":       "0x" + strOrEmpty(b.PrevHash),
		"timestamp":        hexBig(b.Ts),
		"transactionsRoot": "0x" + b.MerkleRoot,
		"miner":            miner,
		"difficulty":       hexBig(diff),
		"totalDifficulty":  hexBig(diff),
		"size":             hexBig(int64(b.Size)),
		"gasLimit":         hexBig(gasLimit),
		"gasUsed":          hexBig(b.GasUsed),
		"baseFeePerGas":    baseFee,
		"nonce":            hexBig(b.Nonce),
		"extraData":        "0x",
		"mixHash":          "0x" + strings.Repeat("00", 32),
		"sha3Uncles":       "0x" + strings.Repeat("00", 32),
		"uncles":           []any{},
		"receiptsRoot":     "0x" + b.MerkleRoot,
		"stateRoot":        "0x" + strings.Repeat("00", 32),
		"logsBloom":        "0x" + strings.Repeat("00", 256),
		"transactionCount": hexBig(int64(b.TxCount)),
		"transactions":     txArr,
	}, nil
}

func getStringFieldAny(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// SubmitEthRawTx 接收標準以太坊 RLP 交易（EIP-155 類型 0），驗證 EIP-155
// 簽名後映射為 TAC 鏈上交易並提交。合約建立交易（to 為空）暫不支援。
func (n *Node) SubmitEthRawTx(rawHex string) (string, error) {
	rawHex = strings.TrimPrefix(strings.TrimSpace(rawHex), "0x")
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return "", fmt.Errorf("eth: raw hex 錯誤: %w", err)
	}
	et, err := crypto.DecodeEthRawTx(raw)
	if err != nil {
		return "", err
	}
	if len(et.To) == 0 {
		return "", errors.New("eth: 合約建立交易不支援；請用 POST /contract/deploy")
	}
	if et.Gas.Int64() > int64(n.contracts.DefaultGas()) {
		return "", fmt.Errorf("eth: gas %d 超過節點上限 %d", et.Gas.Int64(), n.contracts.DefaultGas())
	}
	// 驗證簽名並恢復簽名者：type 2 用 yParity（recid）；type 0 用 EIP-155 v。
	payload := et.Payload()
	var pub *btcec.PublicKey
	if et.TxType == 2 {
		if et.ChainID == nil || et.ChainID.Int64() != crypto.EthChainID {
			return "", fmt.Errorf("eth: chainId %v 不匹配本鏈 %d", et.ChainID, crypto.EthChainID)
		}
		pub, err = crypto.RecoverEthSignerType2(payload, et.R, et.S, et.V)
	} else {
		chainID := int64(0)
		if et.V.Int64() >= 35 {
			chainID = crypto.EthChainID
		}
		pub, err = crypto.RecoverEthSigner(payload, et.R, et.S, et.V, chainID)
	}
	if err != nil {
		return "", err
	}
	fromHash := crypto.Hash160(pub.SerializeCompressed())
	from := crypto.Hash160ToAddress(fromHash)

	to, err := evmToTx0("0x" + hex.EncodeToString(et.To))
	if err != nil {
		return "", err
	}

	// memo：data 非空 → vm:call:<gas>:<data>（鏈上合約呼叫）；空 → 普通轉帳。
	memo := ""
	if len(et.Data) > 0 {
		memo = fmt.Sprintf("vm:call:%d:%s", et.Gas.Int64(), hex.EncodeToString(et.Data))
	}
	// 鏈上金額為 TACm 十進位（1e18 wei）；eth value/fee 為 wei，需換算。
	amount := weiToTacmStr(et.Value)

	// 有效 gas 價（EIP-1559）：min(maxFee, baseFee+priorityFee)；legacy＝gasPrice。
	// fee = effectiveGasPrice × gas；並帶 max_fee/priority_fee/gas_limit 供鏈上驗證。
	// 與 SubmitTransaction 驗證一致：用「下塊 base fee」（由父塊 gas 使用率調整）。
	// 與 SubmitTransaction 驗證完全一致：GasLimit<=0 時 base 不調整（創世/早期塊）。
	baseFee := chaindb.InitialBaseFee
	parentGasUsed, parentGasLimit := int64(0), int64(0)
	if lb, lerr := n.db.GetLatestBlock(); lerr == nil && lb != nil {
		if bf, berr := strconv.ParseFloat(lb.BaseFee, 64); berr == nil && bf > 0 {
			baseFee = bf
		}
		parentGasUsed, parentGasLimit = lb.GasUsed, lb.GasLimit
	}
	nextBase := baseFee
	if parentGasLimit > 0 {
		nextBase = chaindb.ComputeNextBaseFee(baseFee, parentGasUsed, parentGasLimit)
	}
	baseWei := tacmToWei(nextBase)
	var feeWei *big.Int
	maxFeeStr, prioStr := "", ""
	switch et.TxType {
	case 2:
		eff := new(big.Int).Add(baseWei, et.MaxPriorityFee)
		if et.MaxFee.Sign() > 0 && eff.Cmp(et.MaxFee) > 0 {
			eff = et.MaxFee
		}
		feeWei = new(big.Int).Mul(et.Gas, eff)
		maxFeeStr = weiToTacmStr(et.MaxFee)
		prioStr = weiToTacmStr(et.MaxPriorityFee)
	default:
		feeWei = new(big.Int).Mul(et.Gas, et.GasPrice)
		maxFeeStr = weiToTacmStr(et.GasPrice)
		prioStr = "0"
	}
	fee := weiToTacmStr(feeWei)

	// signature 內嵌 v=recid（0/1）＋r||s。
	sigBytes := make([]byte, 65)
	et.R.FillBytes(sigBytes[:32])
	et.S.FillBytes(sigBytes[32:64])
	var recid byte
	if et.TxType == 2 {
		recid = byte(et.V.Int64() & 0x01) // yParity
	} else if et.V.Int64() >= 35 {
		recid = byte((et.V.Int64() - 35) & 0x01)
	} else {
		recid = byte(et.V.Int64() - 27)
	}
	sigBytes[64] = recid

	tx := map[string]any{
		"from":      from,
		"to":        to,
		"amount":    amount,
		"fee":       fee,
		"nonce":     et.Nonce.Int64(),
		"ts":        time.Now().Unix(),
		"memo":      memo,
		"pubkey":    "eth:" + hex.EncodeToString(payload) + ":" + hex.EncodeToString(pub.SerializeCompressed()),
		"signature": "eth:" + hex.EncodeToString(sigBytes),
	}
	// 僅 type2（EIP-1559）帶 gas 參數供鏈上驗證；type0 legacy 維持固定費語義。
	if et.TxType == 2 {
		tx["max_fee"] = maxFeeStr
		tx["priority_fee"] = prioStr
		tx["gas_limit"] = et.Gas.Int64()
	}
	return n.SubmitTransaction(tx)
}

// verifyTxSignature 統一驗證交易簽名：eth: 標記走 crypto.VerifyEthTx
// （EIP-155 恢復驗證），其餘走 TAC 標準 DoubleSHA256+DER 驗證。
func verifyTxSignature(tx map[string]any, signature, from string) bool {
	if strings.HasPrefix(getStringFieldAny(tx, "pubkey"), "eth:") &&
		strings.HasPrefix(signature, "eth:") {
		return crypto.VerifyEthTx(tx)
	}
	return crypto.VerifyTransactionSignature(tx, signature, from)
}

// weiToTacmStr 把 wei（1e-18 TACm）轉 TACm 十進位字串（含小數，去尾零）。
func weiToTacmStr(v *big.Int) string {
	base := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	q, r := new(big.Int).QuoRem(v, base, new(big.Int))
	if r.Sign() == 0 {
		return q.String()
	}
	frac := fmt.Sprintf("%018d", r)
	frac = strings.TrimRight(frac, "0")
	return q.String() + "." + frac
}

// ---- M75-2：交易收據／事件日誌／開發者介面補全 ----

// ethGasPrice 回傳目前建議 gas 價（base fee＋建議 tip，wei 單位）。
func (s *RPCServer) ethGasPrice() (string, error) {
	st := s.node.GetStatus()
	baseFee := st.BaseFee
	if baseFee <= 0 {
		baseFee = chaindb.InitialBaseFee
	}
	price := baseFee + 0.000000001 // 建議小費 1e-9 TACm/gas（M74-3 同源）
	return feeToWeiHex(price), nil
}

// tacmToWei 把 TACm 十進位費率轉為 wei（×1e18）big.Int。
func tacmToWei(f float64) *big.Int {
	rat := new(big.Rat).SetFloat64(f)
	if rat == nil || rat.Num().BitLen() == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).Quo(
		new(big.Int).Mul(rat.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
		rat.Denom())
}

// feeToWeiHex 把 TACm 十進位費率轉為 wei（×1e18）0x hex。
func feeToWeiHex(f float64) string {
	rat := new(big.Rat).SetFloat64(f)
	if rat == nil || rat.Num().BitLen() == 0 {
		return "0x0"
	}
	wei := new(big.Int).Quo(
		new(big.Int).Mul(rat.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
		rat.Denom())
	return "0x" + wei.Text(16)
}

// ethTxObject 以太標準交易 JSON。
func (s *RPCServer) ethTxObject(t *chaindb.Transaction) map[string]any {
	from0x, _ := tx0To0x(t.FromAddr)
	to0x, _ := tx0To0x(t.ToAddr)
	gas := t.GasLimit
	if gas <= 0 {
		gas = chaindb.TxGasBase
	}
	gasPrice := "0x0"
	if t.MaxFee != "" {
		if mf, err := strconv.ParseFloat(t.MaxFee, 64); err == nil {
			gasPrice = feeToWeiHex(mf)
		}
	}
	value := "0x0"
	if t.Amount != "" {
		value = tacmStrToWeiHex(t.Amount)
	}
	input := "0x"
	if strings.HasPrefix(t.Memo, "vm:call:") {
		if parts := strings.Split(t.Memo, ":"); len(parts) >= 4 {
			input = "0x" + parts[len(parts)-1]
		}
	}
	// M131-A：EIP-1559 type-2 欄位（ethers.js v6 / viem 嚴格解析需要 chainId/type）。
	maxFee := "0x0"
	if t.MaxFee != "" {
		if mf, err := strconv.ParseFloat(t.MaxFee, 64); err == nil {
			maxFee = feeToWeiHex(mf)
		}
	}
	priorityFee := "0x0"
	if t.PriorityFee != "" {
		if pf, err := strconv.ParseFloat(t.PriorityFee, 64); err == nil {
			priorityFee = feeToWeiHex(pf)
		}
	}
	txType := "0x0"
	if t.MaxFee != "" || t.PriorityFee != "" {
		txType = "0x2"
	}
	return map[string]any{
		"hash":                 "0x" + t.TxHash,
		"blockHash":            "0x" + t.BlockHash,
		"blockNumber":          hexBig(t.BlockHeight),
		"transactionIndex":     hexBig(int64(t.TxIndex)),
		"from":                 from0x,
		"to":                   to0x,
		"value":                value,
		"gas":                  hexBig(gas),
		"gasPrice":             gasPrice,
		"maxFeePerGas":         maxFee,
		"maxPriorityFeePerGas": priorityFee,
		"type":                 txType,
		"chainId":              "0x" + strconv.FormatInt(crypto.EthChainID, 16),
		"input":                input,
		"nonce":                hexBig(t.Nonce),
		"v":                    "0x0",
		"r":                    "0x0",
		"s":                    "0x0",
		"yParity":              "0x0",
	}
}

// tacmStrToWeiHex 把 TACm 十進位字串（可含小數）轉 wei 0x hex。
func tacmStrToWeiHex(s string) string {
	if s == "" || s == "0" {
		return "0x0"
	}
	rat, ok := new(big.Rat).SetString(s)
	if !ok || rat.Sign() < 0 {
		return "0x0"
	}
	base := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	wei := new(big.Int).Quo(new(big.Int).Mul(rat.Num(), base), rat.Denom())
	return "0x" + wei.Text(16)
}

func (s *RPCServer) ethGetTransactionByHash(params []any) (any, error) {
	hash := strip0x(paramString(params, 0))
	tx, err := s.node.db.GetTransaction(hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, nil // 未找到回 null（以太慣例）
	}
	return s.ethTxObject(tx), nil
}

func (s *RPCServer) ethGetTransactionReceipt(params []any) (any, error) {
	hash := strip0x(paramString(params, 0))
	tx, err := s.node.db.GetTransaction(hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, nil
	}
	return s.ethReceiptForTx(tx)
}

// ethReceiptForTx 以太標準收據物件（M133 抽出，供 getTransactionReceipt / getBlockReceipts 共用）。
func (s *RPCServer) ethReceiptForTx(tx *chaindb.Transaction) (map[string]any, error) {
	from0x, _ := tx0To0x(tx.FromAddr)
	to0x, _ := tx0To0x(tx.ToAddr)
	logs, err := s.node.db.GetLogs(chaindb.LogFilter{TxHash: tx.TxHash})
	if err != nil {
		return nil, err
	}
	ethLogs := make([]any, 0, len(logs))
	for i := range logs {
		ethLogs = append(ethLogs, ethLogObject(&logs[i]))
	}
	status := "0x1"
	if tx.Status != "" && tx.Status != "confirmed" {
		status = "0x0"
	}
	gasUsed := tx.GasUsed
	if gasUsed <= 0 {
		gasUsed = tx.GasLimit
	}
	gasPrice := "0x0"
	if tx.MaxFee != "" {
		if mf, err := strconv.ParseFloat(tx.MaxFee, 64); err == nil {
			gasPrice = feeToWeiHex(mf)
		}
	}
	// M130-B：合約建立交易（to 空）回 null；一般交易同以太語意。
	contractAddr := any(nil)
	if tx.ToAddr == "" {
		contractAddr = "0x0"
	}
	// M131-A：receipt type（2=EIP-1559）/ logsBloom（無 bloom 索引回零值，相容欄位）。
	txType := "0x0"
	if tx.MaxFee != "" || tx.PriorityFee != "" {
		txType = "0x2"
	}
	return map[string]any{
		"transactionHash":   "0x" + tx.TxHash,
		"transactionIndex":  hexBig(int64(tx.TxIndex)),
		"blockHash":         "0x" + tx.BlockHash,
		"blockNumber":       hexBig(tx.BlockHeight),
		"from":              from0x,
		"to":                to0x,
		"status":            status,
		"gasUsed":           hexBig(gasUsed),
		"cumulativeGasUsed": hexBig(gasUsed), // 單鏈：累計 gas 同該交易 gasUsed（相容欄位）
		"contractAddress":   contractAddr,
		"effectiveGasPrice": gasPrice,
		"type":              txType,
		"logsBloom":         "0x" + strings.Repeat("00", 256),
		"logs":              ethLogs,
	}, nil
}

// ethGetBlockReceipts（EIP-2440 主流面）：回指定區塊內全部交易收據。
func (s *RPCServer) ethGetBlockReceipts(params []any) (any, error) {
	blk, err := s.blockByParam(params, 0)
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return []any{}, nil
	}
	txs, err := s.node.db.GetTransactionsByBlock(blk.Height)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(txs))
	for _, tx := range txs {
		r, rerr := s.ethReceiptForTx(tx)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, r)
	}
	return out, nil
}

// blockByParam 解析 block 參數：0x 開頭 66 字元視為 hash，其餘為高度 tag（latest/pending/earliest/hex）。
func (s *RPCServer) blockByParam(params []any, idx int) (*chaindb.Block, error) {
	tag := paramString(params, idx)
	if len(tag) == 66 && strings.HasPrefix(tag, "0x") {
		return s.node.db.GetBlockByHash(strip0x(tag))
	}
	latest, err := s.node.db.GetLatestBlock()
	if err != nil || latest == nil {
		return nil, errors.New("eth: 無區塊")
	}
	switch tag {
	case "", "latest", "pending":
		return latest, nil
	case "earliest":
		return s.node.db.GetBlock(0)
	}
	if h := parseEthBlock(tag, latest.Height); h >= 0 {
		return s.node.db.GetBlock(h)
	}
	return nil, errors.New("eth: block 參數解析失敗")
}

// strip0x 移除 0x 前綴（主流工具 hash 參數必帶 0x；DB 存純 hash）。
func strip0x(s string) string {
	return strings.TrimPrefix(s, "0x")
}

// ethLogObject 以太標準 log JSON。
func ethLogObject(l *chaindb.LogRow) map[string]any {
	data := strings.TrimPrefix(l.Data, "0x")
	return map[string]any{
		"address":          l.Address,
		"topics":           l.Topics,
		"data":             "0x" + data,
		"blockNumber":      hexBig(l.BlockHeight),
		"transactionHash":  "0x" + l.TxHash,
		"transactionIndex": hexBig(int64(l.TxIndex)),
		"logIndex":         hexBig(int64(l.LogIndex)),
		"removed":          false,
	}
}

func (s *RPCServer) ethGetBlockByHash(params []any) (any, error) {
	hash := strings.TrimPrefix(paramString(params, 0), "0x")
	b, err := s.node.db.GetBlockByHash(hash)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("eth: 無此區塊")
	}
	return s.ethBlockObject(b, paramBool(params, 1))
}

func (s *RPCServer) ethGetStorageAt(params []any) (string, error) {
	addr, err := s.node.normalizeEthAddr(paramString(params, 0))
	if err != nil {
		return "", err
	}
	addr0x, err := tx0To0x(addr)
	if err != nil {
		return "", err
	}
	pos := new(big.Int)
	if p := paramString(params, 1); p != "" {
		pos, _ = new(big.Int).SetString(strings.TrimPrefix(p, "0x"), 16)
		if pos == nil {
			pos = new(big.Int)
		}
	}
	v := s.node.contracts.StorageAt(addr0x, pos)
	return "0x" + pad32Hex(v), nil
}

// pad32Hex 把 big.Int 格式化為 32 字節 hex（以太 storage 慣例）。
func pad32Hex(v *big.Int) string {
	b := v.Bytes()
	if len(b) >= 32 {
		return hex.EncodeToString(b[len(b)-32:])
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return hex.EncodeToString(out)
}

// M151d：web3_sha3 —— 主流工具（ethers/viem）常呼叫；keccak256(bytes)。
func (s *RPCServer) web3Sha3(params []any) (string, error) {
	data := paramString(params, 0)
	b, err := hexInput(data)
	if err != nil {
		return "", fmt.Errorf("eth: data hex 錯誤: %w", err)
	}
	return "0x" + hex.EncodeToString(crypto.Keccak256(b)), nil
}

// M151d：eth_createAccessList（EIP-2930）——主流錢包發送前會先取 access list。
// 本鏈 EVM 執行不需預先宣告存取清單，回 from/to 兩項空 storageKeys 並附模擬 gasUsed。
func (s *RPCServer) ethCreateAccessList(params []any) (any, error) {
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
		return nil, fmt.Errorf("eth: data hex 錯誤: %w", err)
	}
	callValue := new(big.Int)
	if v := getStringFieldAny(callObj, "value"); v != "" {
		callValue, _ = new(big.Int).SetString(strings.TrimPrefix(v, "0x"), 16)
		if callValue == nil {
			return nil, errors.New("eth: value 解析失敗")
		}
	}
	accessList := make([]map[string]any, 0, 2)
	seen := make(map[string]bool)
	addEntry := func(addr string) {
		if addr == "" || seen[addr] {
			return
		}
		seen[addr] = true
		accessList = append(accessList, map[string]any{
			"address":     addr,
			"storageKeys": []string{},
		})
	}
	addEntry(caller0x)
	addEntry(to0x)

	gasUsed := "0x0"
	if r, rerr := s.node.contracts.SimulateCallGas(to0x, caller0x, calldata, callValue, s.node.contracts.DefaultGas()); rerr == nil && r.OK && !r.Reverted {
		gasUsed = "0x" + strconv.FormatUint(r.GasUsed, 16)
	}
	return map[string]any{
		"accessList": accessList,
		"gasUsed":    gasUsed,
	}, nil
}

func (s *RPCServer) ethEstimateGas(params []any) (string, error) {
	callObj, ok := params[0].(map[string]any)
	if !ok {
		return "", errors.New("eth: params[0] 需為 call object")
	}
	to := getStringFieldAny(callObj, "to")
	from := getStringFieldAny(callObj, "from")
	data := getStringFieldAny(callObj, "data")
	toTx0, err := s.node.normalizeEthAddr(to)
	if err != nil {
		return "", err
	}
	to0x, err := tx0To0x(toTx0)
	if err != nil {
		return "", err
	}
	caller0x := "0x0000000000000000000000000000000000000000"
	if from != "" {
		fTx0, ferr := s.node.normalizeEthAddr(from)
		if ferr != nil {
			return "", ferr
		}
		caller0x, err = tx0To0x(fTx0)
		if err != nil {
			return "", err
		}
	}
	// M132-A：estimateGas 支援 value（wei hex），與 eth_call 一致（主流工具常帶）。
	callValue := new(big.Int)
	if v := getStringFieldAny(callObj, "value"); v != "" {
		callValue, _ = new(big.Int).SetString(strings.TrimPrefix(v, "0x"), 16)
		if callValue == nil {
			return "", errors.New("eth: value 解析失敗")
		}
	}
	calldata, err := hexInput(data)
	if err != nil {
		return "", fmt.Errorf("eth: data hex 錯誤: %w", err)
	}
	r, err := s.node.contracts.SimulateCallGas(to0x, caller0x, calldata, callValue, s.node.contracts.DefaultGas())
	if err != nil {
		return "", err
	}
	if !r.OK || r.Reverted {
		return "", errors.New("eth: estimateGas reverted: " + r.RevertReason)
	}
	used := r.GasUsed
	if used == 0 {
		used = chaindb.TxGasBase
	}
	return hexBig(int64(used)), nil
}

// parseEthBlock 解析 fromBlock/toBlock（"latest"/"earliest"/hex）→ 高度。
func parseEthBlock(v string, latest int64) int64 {
	switch v {
	case "", "latest", "pending":
		return latest
	case "earliest":
		return 0
	}
	if n, err := strconv.ParseInt(strings.TrimPrefix(v, "0x"), 16, 64); err == nil {
		return n
	}
	return 0
}

// normalizeEth0x 統一地址/topic 為小寫 0x 前綴。
func normalizeEth0x(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "0x") {
		v = "0x" + v
	}
	return strings.ToLower(v)
}

func (s *RPCServer) ethGetLogs(params []any) (any, error) {
	filterObj, ok := params[0].(map[string]any)
	if !ok {
		return nil, errors.New("eth: params[0] 需為 filter object")
	}
	f := chaindb.LogFilter{Limit: 10000}
	if latest, err := s.node.db.GetLatestBlock(); err == nil && latest != nil {
		if fb := getStringFieldAny(filterObj, "fromBlock"); fb != "" {
			f.FromBlock = parseEthBlock(fb, latest.Height)
		}
		if tb := getStringFieldAny(filterObj, "toBlock"); tb != "" {
			f.ToBlock = parseEthBlock(tb, latest.Height)
		}
		// M132-A：blockHash 過濾（ethers/viem 常用）——定位該區塊高度後鎖定單塊查詢。
		if bh := getStringFieldAny(filterObj, "blockHash"); bh != "" {
			blk, berr := s.node.db.GetBlockByHash(strip0x(bh))
			if berr != nil {
				return nil, berr
			}
			if blk == nil {
				return []any{}, nil
			}
			f.FromBlock = blk.Height
			f.ToBlock = blk.Height
		}
	}
	// M128：address 支援單值或陣列（任一匹配）。
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
	// M128：topics 支援多層（層內 OR、跨層 AND）。
	if ts := filterObj["topics"]; ts != nil {
		if arr, ok := ts.([]any); ok {
			f.Topics = parseEthTopics(arr)
		}
	}
	logs, err := s.node.db.GetLogs(f)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(logs))
	for i := range logs {
		out = append(out, ethLogObject(&logs[i]))
	}
	return out, nil
}
