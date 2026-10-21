package node

// M155 索引 API（Indexer）——Etherscan 風公共查詢面（只讀）。
// 端點：
//   GET /api/v1/stats          鏈統計（高度/區塊數/交易數/帳戶數/mempool）
//   GET /api/v1/blocks?limit=&offset=   區塊列表（高度降序）
//   GET /api/v1/block/{id}     單一區塊（高度或哈希）＋其交易
//   GET /api/v1/tx/{hash}      單一交易
//   GET /api/v1/txs?address=&limit=&offset=&dir=   地址交易歷史
//   GET /api/v1/address/{addr} 地址帳戶（餘額/nonce/首末活躍）＋交易數
//   GET /api/v1/delegations           委託紀錄分頁（M158）
//   GET /api/v1/delegations/{delegator}  委託人委託＋累計委託獎勵
//   GET /api/v1/validators            驗證人排名（權重/委託總額/委託人數，M160）
// 全部走 chaindb 既有只讀查詢，不碰共識/打包/寫入路徑。

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

var txHashRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// pagination 解析 limit/offset（帶上限防護）。
func pagination(r *http.Request, defLimit, maxLimit int) (int, int) {
	limit := defLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	return limit, offset
}

// handleIndexerStats GET /api/v1/stats — 鏈統計。
func (s *RPCServer) handleIndexerStats(w http.ResponseWriter, r *http.Request) {
	db := s.node.db
	height := db.GetTipHeight()
	blockCount, err1 := db.GetBlockCount()
	txCount, err2 := db.CountAllTransactions()
	acctCount, err3 := db.CountAccounts()
	for _, e := range []error{err1, err2, err3} {
		if e != nil {
			writeErr(w, http.StatusInternalServerError, "indexer: "+e.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "height": height, "block_count": blockCount,
		"tx_count": txCount, "account_count": acctCount,
		"mempool_size": db.MempoolSize(),
	})
}

// handleIndexerBlocks GET /api/v1/blocks — 區塊列表。
func (s *RPCServer) handleIndexerBlocks(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 20, 100)
	blocks, err := s.node.db.GetBlocks(limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	total, err := s.node.db.GetBlockCount()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "total": total, "limit": limit, "offset": offset, "blocks": blocks})
}

// handleIndexerBlock GET /api/v1/block/{id} — 單一區塊（高度或哈希）＋交易。
func (s *RPCServer) handleIndexerBlock(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var blk *chaindb.Block
	var err error
	if n, perr := strconv.ParseInt(id, 10, 64); perr == nil {
		blk, err = s.node.db.GetBlock(n)
	} else {
		blk, err = s.node.db.GetBlockByHash(id)
	}
	if err != nil || blk == nil {
		writeErr(w, http.StatusNotFound, "block not found")
		return
	}
	txs, err := s.node.db.GetTransactionsByBlock(blk.Height)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "block": blk, "txs": txs})
}

// handleIndexerTx GET /api/v1/tx/{hash} — 單一交易。
func (s *RPCServer) handleIndexerTx(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSpace(r.PathValue("hash"))
	if !txHashRe.MatchString(hash) {
		writeErr(w, http.StatusBadRequest, "invalid tx hash")
		return
	}
	tx, err := s.node.db.GetTransaction(hash)
	if err != nil || tx == nil {
		writeErr(w, http.StatusNotFound, "tx not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tx": tx})
}

// handleIndexerTxs GET /api/v1/txs?address=&limit=&offset=&dir= — 地址交易歷史。
func (s *RPCServer) handleIndexerTxs(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "address required")
		return
	}
	if !crypto.IsValidAddress(addr) {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir != "in" && dir != "out" {
		dir = ""
	}
	limit, offset := pagination(r, 20, 100)
	txs, err := s.node.db.GetTransactionsByAddressPage(addr, offset, limit, dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	total, err := s.node.db.CountTransactionsByAddress(addr, dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "address": addr, "total": total, "limit": limit, "offset": offset, "txs": txs})
}

// handleIndexerAddress GET /api/v1/address/{addr} — 地址帳戶與活動概覽。
func (s *RPCServer) handleIndexerAddress(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.PathValue("addr"))
	if !crypto.IsValidAddress(addr) {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	acct, err := s.node.db.GetAccount(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	txCount, err := s.node.db.CountTransactionsByAddress(addr, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	if acct == nil {
		acct = &chaindb.Account{Address: addr, Balance: "0", Nonce: 0}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "account": acct, "tx_count": txCount,
	})
}


// handleIndexerDelegations GET /api/v1/delegations?limit=&offset=
// 全鏈委託紀錄分頁（委託額>0，amount 降序）。
func (s *RPCServer) handleIndexerDelegations(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r, 20, 100)
	dels, err := s.node.db.ListDelegations(limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	total, err := s.node.db.CountDelegations()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "delegations": dels, "total": total,
		"limit": limit, "offset": offset,
	})
}

// handleIndexerDelegator GET /api/v1/delegations/{delegator}
// 委託人全部委託＋累計委託獎勵（coinbase:deleg）。
func (s *RPCServer) handleIndexerDelegator(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.PathValue("delegator"))
	if !crypto.IsValidAddress(addr) {
		writeErr(w, http.StatusBadRequest, "invalid address")
		return
	}
	dels, err := s.node.db.GetDelegations(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	reward, err := s.node.db.GetDelegationRewardTotal(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	rewardTxs, err := s.node.db.CountDelegationRewards()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "delegator": addr, "delegations": dels,
		"deleg_rewards": reward, "deleg_reward_txs": rewardTxs,
	})
}

// handleIndexerValidators GET /api/v1/validators — 驗證人排名（權重降序）。
func (s *RPCServer) handleIndexerValidators(w http.ResponseWriter, r *http.Request) {
	vs, err := s.node.db.ListValidatorsWithDelegations()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "indexer: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "validators": vs, "total": len(vs),
	})
}
