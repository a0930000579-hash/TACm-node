package nodeui

// v53-B1/A4：nodeweb 只讀 eth JSON-RPC（節點端查詢面）。
// 與主鏈 eth_rpc.go 同語義但僅含只讀方法（無 eth_sendTransaction/sendRawTransaction
// 等寫入）——任一節點皆可被查詢（與主流鏈節點同級），寫入仍由主鏈 tacweb 處理。
//
// 支援：web3_clientVersion / net_version / eth_chainId / net_peerCount /
// eth_blockNumber / eth_gasPrice / eth_getBalance / eth_getTransactionCount /
// eth_getBlockByNumber / eth_getBlockByHash / eth_getTransactionByHash /
// eth_getTransactionReceipt / eth_getBlockTransactionCountByNumber /
// admin_peers / debug_getRawBlock（batch 亦支援）。

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

type ethReadReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type ethReadResp struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (s *Server) handleEthReadonly(w http.ResponseWriter, r *http.Request) {
	// 讀取 body（上限 1MB 防灌水）。
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	raw := strings.TrimSpace(string(buf[:n]))
	if raw == "" {
		writeEthReadErr(w, nil, -32700, "parse error")
		return
	}
	if raw[0] == '[' {
		var reqs []ethReadReq
		if err := json.Unmarshal([]byte(raw), &reqs); err != nil {
			writeEthReadErr(w, nil, -32700, "parse error")
			return
		}
		out := make([]ethReadResp, 0, len(reqs))
		for _, req := range reqs {
			out = append(out, s.dispatchEthRead(req))
		}
		writeEthReadJSON(w, out)
		return
	}
	var req ethReadReq
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		writeEthReadErr(w, nil, -32700, "parse error")
		return
	}
	writeEthReadJSON(w, s.dispatchEthRead(req))
}

func writeEthReadErr(w http.ResponseWriter, id any, code int, msg string) {
	writeEthReadJSON(w, ethReadResp{JSONRPC: "2.0", ID: id, Error: &struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: msg}})
}

func writeEthReadJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[nodeui] eth encode: %v", err)
	}
}

func (s *Server) dispatchEthRead(req ethReadReq) ethReadResp {
	resp := ethReadResp{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" {
		resp.Error = &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{Code: -32600, Message: "jsonrpc must be 2.0"}
		return resp
	}
	out, err := s.ethReadMethod(req.Method, req.Params)
	if err != nil {
		resp.Error = &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{Code: -32603, Message: err.Error()}
		return resp
	}
	resp.Result = out
	return resp
}

func (s *Server) ethReadMethod(method string, params []any) (any, error) {
	st := s.n.GetStatus()
	switch method {
	case "trace_filter":
		out, err := s.rpc.TraceFilterPublic(params)
		return out, err
	case "trace_block", "debug_traceBlockByNumber":
		out, err := s.rpc.DebugTraceBlockPublic(params, false)
		return out, err
	case "debug_traceBlockByHash":
		out, err := s.rpc.DebugTraceBlockPublic(params, true)
		return out, err
	case "trace_replayBlockTransactions":
		out, err := s.rpc.TraceReplayBlockPublic(params)
		return out, err
	case "trace_replayTransaction":
		out, err := s.rpc.TraceReplayTransactionPublic(params)
		return out, err
	case "trace_rawTransaction":
		out, err := s.rpc.TraceRawTransactionPublic(params)
		return out, err
	case "trace_callMany":
		out, err := s.rpc.TraceCallManyPublic(params)
		return out, err
	case "trace_transaction":
		out, err := s.rpc.TraceTransactionPublic(params)
		return out, err
	case "debug_traceCall", "trace_call":
		out, err := s.rpc.DebugTraceCallPublic(params)
		return out, err
	case "debug_traceTransaction":
		out, err := s.rpc.DebugTraceTransactionPublic(params)
		return out, err
	case "debug_getRawReceipts":
		out, err := s.rpc.DebugGetRawReceiptsPublic(params)
		return out, err
	case "debug_storageRangeAt":
		out, err := s.rpc.DebugStorageRangeAtPublic(params)
		return out, err
	case "web3_clientVersion":
		return "tacm-go/1.0", nil
	case "net_version", "eth_chainId":
		return "0x" + strings.TrimPrefix(st.ChainID, "0x"), nil
	case "net_peerCount":
		c := 0
		if pn := s.n.P2PNet(); pn != nil {
			for _, p := range pn.PeerSnapshot() {
				if p.Online {
					c++
				}
			}
		}
		return "0x" + strconv.Itoa(c), nil
	case "eth_blockNumber":
		return "0x" + strconv.FormatInt(st.BlockHeight, 16), nil
	case "eth_gasPrice":
		return "0x" + strconv.FormatUint(uint64(st.GasPrice*1e18), 16), nil
	case "eth_getBalance":
		addr := s.ethParam(params, 0)
		acc, err := s.n.Wallet().Balance(addr)
		if err != nil || acc == nil || acc.TACmBalance == nil {
			return "0x0", nil
		}
		return tacmToWeiHex(acc.TACmBalance.String()), nil
	case "eth_getTransactionCount":
		addr := s.ethParam(params, 0)
		return "0x" + strconv.FormatInt(s.n.DB().GetNonce(addr), 16), nil
	case "eth_getBlockByNumber":
		return s.ethReadBlockByNumber(params)
	case "eth_getBlockByHash":
		return s.ethReadBlockByHash(params)
	case "eth_getTransactionByHash":
		return s.ethReadTx(params)
	case "eth_getTransactionReceipt":
		return s.ethReadReceipt(params)
	case "eth_getBlockTransactionCountByNumber":
		h := s.n.DB().GetTipHeight()
		if tag := s.ethParam(params, 0); tag != "" && tag != "latest" && tag != "pending" {
			if n2, err := strconv.ParseInt(strings.TrimPrefix(tag, "0x"), 16, 64); err == nil {
				h = n2
			}
		}
		b, err := s.n.DB().GetBlock(h)
		if err != nil || b == nil {
			return nil, nil
		}
		return "0x" + strconv.Itoa(b.TxCount), nil
	case "admin_peers":
		out := []map[string]any{}
		if pn := s.n.P2PNet(); pn != nil {
			for _, p := range pn.PeerSnapshot() {
				out = append(out, map[string]any{
					"id": p.NodeID, "base_url": p.BaseURL, "owner": p.Owner,
					"height": p.Height, "finalized_height": p.Finalized,
					"online": p.Online, "last_seen": p.LastSeen.Format(time.RFC3339),
				})
			}
		}
		return out, nil
	case "debug_getRawBlock":
		return s.ethReadRawBlock(params)
	}
	return nil, nil // 未知方法回 null（保守，不洩漏方法面）
}

func (s *Server) ethParam(params []any, i int) string {
	if i >= len(params) {
		return ""
	}
	if v, ok := params[i].(string); ok {
		return v
	}
	return ""
}

func (s *Server) ethReadBlockByNumber(params []any) (map[string]any, error) {
	tag := s.ethParam(params, 0)
	bb, err := s.n.DB().GetLatestBlock()
	if err != nil || bb == nil {
		return nil, nil
	}
	if tag != "" && tag != "latest" && tag != "pending" {
		if h, err2 := strconv.ParseInt(strings.TrimPrefix(tag, "0x"), 16, 64); err2 == nil {
			if gb, err3 := s.n.DB().GetBlock(h); err3 == nil && gb != nil {
				bb = gb
			}
		}
	}
	return s.ethBlockObj(bb), nil
}

func (s *Server) ethReadBlockByHash(params []any) (map[string]any, error) {
	hash := strings.TrimPrefix(s.ethParam(params, 0), "0x")
	bb, err := s.n.DB().GetBlockByHash(hash)
	if err != nil || bb == nil {
		return nil, nil
	}
	return s.ethBlockObj(bb), nil
}

func (s *Server) ethBlockObj(b *chaindb.Block) map[string]any {
	txs, _ := s.n.DB().GetTransactionsByBlock(b.Height)
	txHashes := make([]any, 0, len(txs))
	for _, tx := range txs {
		txHashes = append(txHashes, "0x"+tx.TxHash)
	}
	parent := "0x0000000000000000000000000000000000000000000000000000000000000000"
	if b.PrevHash != nil && *b.PrevHash != "" {
		parent = "0x" + strings.TrimPrefix(*b.PrevHash, "0x")
	}
	return map[string]any{
		"number":           "0x" + strconv.FormatInt(b.Height, 16),
		"hash":             "0x" + b.Hash,
		"parentHash":       parent,
		"timestamp":        "0x" + strconv.FormatInt(b.Ts, 16),
		"miner":            "0x" + b.ProposerAddress,
		"transactions":     txHashes,
		"transactionCount": "0x" + strconv.Itoa(len(txs)),
		"size":             "0x" + strconv.Itoa(b.Size),
		"gasUsed":          "0x" + strconv.FormatInt(b.GasUsed, 16),
		"gasLimit":         "0x" + strconv.FormatInt(b.GasLimit, 16),
		"difficulty":       "0x" + strconv.Itoa(b.Difficulty),
		"totalDifficulty":  "0x" + strconv.Itoa(b.Difficulty),
		"uncles":           []any{},
	}
}

// ethReadTx 交易物件（hash/from/to/value/gas/nonce/blockNumber）。
func (s *Server) ethReadTx(params []any) (map[string]any, error) {
	hash := strings.TrimPrefix(s.ethParam(params, 0), "0x")
	tx, err := s.n.DB().GetTransaction(hash)
	if err != nil || tx == nil {
		return nil, nil
	}
	from0x, _ := s.tx0To0x(tx.FromAddr)
	to0x, _ := s.tx0To0x(tx.ToAddr)
	return map[string]any{
		"hash":             "0x" + tx.TxHash,
		"from":             from0x,
		"to":               to0x,
		"value":            tacmToWeiHex(tx.Amount),
		"gas":              "0x" + strconv.FormatInt(tx.GasLimit, 16),
		"gasPrice":         "0x0",
		"nonce":            "0x" + strconv.FormatInt(tx.Nonce, 16),
		"blockNumber":      "0x" + strconv.FormatInt(tx.BlockHeight, 16),
		"blockHash":        "0x" + tx.BlockHash,
		"transactionIndex": "0x" + strconv.Itoa(tx.TxIndex),
		"input":            "0x",
		"timestamp":        "0x" + strconv.FormatInt(tx.Ts, 16),
	}, nil
}

// ethReadReceipt 收據物件（status/logs/gasUsed/blockNumber）。
func (s *Server) ethReadReceipt(params []any) (map[string]any, error) {
	hash := strings.TrimPrefix(s.ethParam(params, 0), "0x")
	tx, err := s.n.DB().GetTransaction(hash)
	if err != nil || tx == nil {
		return nil, nil
	}
	from0x, _ := s.tx0To0x(tx.FromAddr)
	to0x, _ := s.tx0To0x(tx.ToAddr)
	logs, _ := s.n.DB().GetLogs(chaindb.LogFilter{TxHash: tx.TxHash})
	return map[string]any{
		"transactionHash":   "0x" + tx.TxHash,
		"transactionIndex":  "0x" + strconv.Itoa(tx.TxIndex),
		"blockNumber":       "0x" + strconv.FormatInt(tx.BlockHeight, 16),
		"blockHash":         "0x" + tx.BlockHash,
		"from":              from0x,
		"to":                to0x,
		"cumulativeGasUsed": "0x" + strconv.FormatInt(tx.GasUsed, 16),
		"gasUsed":           "0x" + strconv.FormatInt(tx.GasUsed, 16),
		"status":            "0x1",
		"logs":              logs,
		"logsBloom":         "0x",
	}, nil
}

// ethReadRawBlock 原始區塊 JSON（debug_getRawBlock）。
func (s *Server) ethReadRawBlock(params []any) (any, error) {
	tag := s.ethParam(params, 0)
	bb, err := s.n.DB().GetLatestBlock()
	if err != nil || bb == nil {
		return nil, nil
	}
	if tag != "" && tag != "latest" && tag != "pending" {
		if strings.HasPrefix(tag, "0x") && len(tag) == 66 {
			if gb, err2 := s.n.DB().GetBlockByHash(strings.TrimPrefix(tag, "0x")); err2 == nil && gb != nil {
				bb = gb
			}
		} else if h, err2 := strconv.ParseInt(strings.TrimPrefix(tag, "0x"), 16, 64); err2 == nil {
			if gb, err3 := s.n.DB().GetBlock(h); err3 == nil && gb != nil {
				bb = gb
			}
		}
	}
	txs, _ := s.n.DB().GetTransactionsByBlock(bb.Height)
	raw, _ := json.Marshal(map[string]any{"block": bb, "transactions": txs})
	return string(raw), nil
}

// tx0To0x 節點地址（tx0 前綴）轉以太 0x 地址（Hash160 hex）。
func (s *Server) tx0To0x(addr string) (string, error) {
	h, err := crypto.AddressToHash160(addr)
	if err != nil {
		return "0x0000000000000000000000000000000000000000", nil
	}
	return "0x" + hex.EncodeToString(h), nil
}

// tacmToWeiHex 把 TACm 字串轉 wei hex（1 TACm = 1e18 wei）。
func tacmToWeiHex(amount string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil {
		return "0x0"
	}
	return "0x" + strconv.FormatUint(uint64(f*1e18), 16)
}
