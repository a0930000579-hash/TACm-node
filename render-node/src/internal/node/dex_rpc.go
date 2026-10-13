package node

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/dex"
	"tacm/internal/vm"
)

// 本檔提供「鏈上 DEX（AMM 恆定乘積交易所）」RPC 端點（M45）：
//   - GET  /dex/pools           —— 池列表（含儲備/價格）
//   - POST /dex/create          —— 建立流動性池
//   - POST /dex/liquidity/add   —— 注入流動性（LP 份額）
//   - POST /dex/liquidity/remove—— 按份額退回流動性
//   - POST /dex/swap            —— 即時兌換（含 0.3% 手續費）
//   - GET  /dex/quote           —— 兌換報價（不執行）
//
// 結算模型（鏈上可審計）：所有代幣移動皆以 VM 標準代幣合約 transfer 上鏈；
// 做市帳戶＝節點金鑰地址（admin），池帳戶＝crypto.DeriveKey 確定性派生密鑰，
// 節點僅在池動作時以池密鑰代簽——符合「發幣→建池→流動性→交易」商業閉環。

// signerForPool 依池與帳戶地址決定簽名密鑰：admin 用節點金鑰，
// 池帳戶用 DeriveKey("tac-dex-pool-<token0>:<token1>") 派生（與建池一致）。
// 查詢只讀 node 自持對映，避免在 DEX 引擎持鎖時重入 Pools() 造成死鎖。
func (n *Node) signerForPool(poolID, addr string) (*crypto.KeyPair, error) {
	if addr == n.nodeAddress {
		return n.keypair, nil
	}
	// 農場金庫標記（引擎層 "tac-dex-stake:<pool>"）→ 派生金庫簽名者。
	if addr == "tac-dex-stake:"+poolID {
		vkp, err := crypto.DeriveKey(n.keypair.PrivateKey(), "tac-dex-stake-"+poolID)
		if err != nil {
			return nil, err
		}
		return vkp, nil
	}
	n.dexPoolMu.Lock()
	key := n.dexPoolKeys[poolID]
	n.dexPoolMu.Unlock()
	if key == "" {
		return nil, fmt.Errorf("未知池簽名者: %s", poolID)
	}
	kp, err := crypto.DeriveKey(n.keypair.PrivateKey(), "tac-dex-pool-"+key)
	if err != nil {
		return nil, err
	}
	derived, err := kp.Address()
	if err != nil {
		return nil, err
	}
	if derived != addr {
		return nil, fmt.Errorf("簽名者與池地址不符: %s", addr)
	}
	return kp, nil
}

// recordDexPool 記錄 poolID→token 對（供 signerForPool 無鎖查詢）。
func (n *Node) recordDexPool(poolID, token0, token1 string) {
	n.dexPoolMu.Lock()
	defer n.dexPoolMu.Unlock()
	if n.dexPoolKeys == nil {
		n.dexPoolKeys = map[string]string{}
	}
	n.dexPoolKeys[poolID] = token0 + ":" + token1
}

// resolveVault 把引擎層金庫標記（"tac-dex-stake:<pool>"）解析為派生金庫地址。
func (n *Node) resolveVault(addr string) string {
	const prefix = "tac-dex-stake:"
	if !strings.HasPrefix(addr, prefix) {
		return addr
	}
	kp, err := crypto.DeriveKey(n.keypair.PrivateKey(), "tac-dex-stake-"+strings.TrimPrefix(addr, prefix))
	if err != nil {
		return addr
	}
	a, err := kp.Address()
	if err != nil {
		return addr
	}
	return a
}

// dexTransfer 執行一筆標準代幣 transfer 並上鏈（DEX 引擎回調）。
func (n *Node) dexTransfer(poolID, from, to, contract string, amount *big.Int) error {
	if n.contracts == nil {
		return errors.New("合約層未啟用")
	}
	// 僅把「收款方」金庫標記解析成真實派生地址；from 保留標記交由
	// signerForPool 的農場金庫分支選出正確簽名者。
	to = n.resolveVault(to)
	kp, err := n.signerForPool(poolID, from)
	if err != nil {
		return err
	}
	to0x, err := tx0To0x(to)
	if err != nil {
		return err
	}
	calldata := vm.Erc20TransferCalldata(to0x, amount)
	h, err := n.submitSignedContractCallAs(kp, contract, hex.EncodeToString(calldata), 0)
	if err != nil {
		return err
	}
	// DEX 引擎需在「轉入後立即讀餘額」，故等待交易入塊（block-time 秒級）。
	return n.waitTxMined(h)
}

// waitTxMined 輪詢交易直至入塊（最長 25 秒；供 DEX 鏈上結算使用，
// 覆蓋並行測試／難度調整下的慢出塊）。
func (n *Node) waitTxMined(txHash string) error {
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		tx, err := n.db.GetTransaction(txHash)
		if err != nil {
			return err
		}
		if tx != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("交易入塊超時: %s", txHash)
}

// dexBalance 讀取持有人對代幣合約的當前餘額（DEX 引擎回調）。
func (n *Node) dexBalance(holder, contract string) (*big.Int, error) {
	if n.contracts == nil {
		return nil, errors.New("合約層未啟用")
	}
	key, err := holderKey(holder)
	if err != nil {
		return nil, err
	}
	evmContract, err := tx0To0x(contract)
	if err != nil {
		return nil, err
	}
	val := n.contracts.StorageAt(evmContract, key)
	return val, nil
}

// poolJSON 供前端顯示的池資訊（含價格）。
type poolJSON struct {
	ID        string `json:"id"`
	Token0    string `json:"token0"`
	Token1    string `json:"token1"`
	PoolAddr  string `json:"pool_addr"`
	Reserve0  string `json:"reserve0"`
	Reserve1  string `json:"reserve1"`
	LPTotal   string `json:"lp_total"`
	CreatedAt int64  `json:"created_at"`
	Price0    string `json:"price0"` // 1 token0 值多少 token1
	Price1    string `json:"price1"` // 1 token1 值多少 token0
}

func poolsToJSON(ps []*dex.Pool) []poolJSON {
	out := make([]poolJSON, 0, len(ps))
	for _, p := range ps {
		j := poolJSON{
			ID: p.ID, Token0: p.Token0, Token1: p.Token1, PoolAddr: p.PoolAddr,
			Reserve0: p.Reserve0.String(), Reserve1: p.Reserve1.String(),
			LPTotal: p.LPTotal.String(), CreatedAt: p.CreatedAt,
		}
		if p.Reserve0.Sign() > 0 && p.Reserve1.Sign() > 0 {
			// 價格以 8 位小數字串呈現（僅展示用途）。
			r0 := new(big.Rat).SetInt(p.Reserve0)
			r1 := new(big.Rat).SetInt(p.Reserve1)
			j.Price0 = new(big.Rat).Quo(r1, r0).FloatString(8)
			j.Price1 = new(big.Rat).Quo(r0, r1).FloatString(8)
		}
		out = append(out, j)
	}
	return out
}

// handleDexPools GET /dex/pools
func (s *RPCServer) handleDexPools(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX 未啟用（需合約層）")
		return
	}
	ps, err := s.node.dex.Pools()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pools": poolsToJSON(ps)})
}

// handleDexCreate POST /dex/create
// body: {token0, token1}
func (s *RPCServer) handleDexCreate(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX 未啟用（需合約層）")
		return
	}
	var req struct {
		Token0 string `json:"token0"`
		Token1 string `json:"token1"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	// 驗證兩幣皆為已部署合約。
	for _, c := range []string{req.Token0, req.Token1} {
		evm, err := tx0To0x(c)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "無效代幣地址: "+c)
			return
		}
		if s.node.contracts.Get(evm) == nil {
			writeErr(w, http.StatusBadRequest, "代幣合約不存在: "+c)
			return
		}
	}
	poolAddr, err := dexPoolAddr(s.node, req.Token0, req.Token1)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	p, err := s.node.dex.CreatePool(req.Token0, req.Token1, poolAddr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.node.recordDexPool(p.ID, req.Token0, req.Token1)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pool": poolsToJSON([]*dex.Pool{p})[0]})
}

// dexPoolAddr 為新池確定性派生池帳戶地址。
func dexPoolAddr(n *Node, token0, token1 string) (string, error) {
	kp, err := crypto.DeriveKey(n.keypair.PrivateKey(),
		"tac-dex-pool-"+token0+":"+token1)
	if err != nil {
		return "", err
	}
	return kp.Address()
}

// handleDexLiquidityAdd POST /dex/liquidity/add
// body: {pool, amount0, amount1}（owner=admin 做市帳戶）
func (s *RPCServer) handleDexLiquidityAdd(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX 未啟用（需合約層）")
		return
	}
	var req struct {
		Pool    string `json:"pool"`
		Amount0 string `json:"amount0"`
		Amount1 string `json:"amount1"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	a0, ok0 := new(big.Int).SetString(req.Amount0, 10)
	a1, ok1 := new(big.Int).SetString(req.Amount1, 10)
	if !ok0 || !ok1 {
		writeErr(w, http.StatusBadRequest, "invalid amount")
		return
	}
	p, err := s.node.dex.AddLiquidity(req.Pool, s.node.nodeAddress, a0, a1)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pos, _ := s.node.dex.PositionOf(req.Pool, s.node.nodeAddress)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "pool": poolsToJSON([]*dex.Pool{p})[0],
		"position": map[string]any{"pool": pos.PoolID, "owner": pos.Owner, "shares": pos.Shares.String()},
	})
}

// handleDexLiquidityRemove POST /dex/liquidity/remove
// body: {pool, shares}
func (s *RPCServer) handleDexLiquidityRemove(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX 未啟用（需合約層）")
		return
	}
	var req struct {
		Pool   string `json:"pool"`
		Shares string `json:"shares"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	sh, ok := new(big.Int).SetString(req.Shares, 10)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid shares")
		return
	}
	p, err := s.node.dex.RemoveLiquidity(req.Pool, s.node.nodeAddress, sh)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	pos, _ := s.node.dex.PositionOf(req.Pool, s.node.nodeAddress)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "pool": poolsToJSON([]*dex.Pool{p})[0],
		"position": map[string]any{"pool": pos.PoolID, "owner": pos.Owner, "shares": pos.Shares.String()},
	})
}

// handleDexSwap POST /dex/swap
// body: {pool, token, amount}（owner=admin）
func (s *RPCServer) handleDexSwap(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX 未啟用（需合約層）")
		return
	}
	var req struct {
		Pool   string `json:"pool"`
		Token  string `json:"token"`
		Amount string `json:"amount"`
		MinOut string `json:"min_out,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON body")
		return
	}
	am, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid amount")
		return
	}
	var minOut *big.Int
	if req.MinOut != "" {
		minOut, ok = new(big.Int).SetString(req.MinOut, 10)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid min_out")
			return
		}
	}
	tOut, out, err := s.node.dex.Swap(req.Pool, s.node.nodeAddress, req.Token, am, minOut)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p, _ := s.node.dex.Get(req.Pool)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "token_out": tOut, "amount_out": out.String(),
		"pool": poolsToJSON([]*dex.Pool{p})[0],
	})
}

// handleDexQuote GET /dex/quote?pool=&token=&amount=
func (s *RPCServer) handleDexQuote(w http.ResponseWriter, r *http.Request) {
	if s.node.dex == nil {
		writeErr(w, http.StatusServiceUnavailable, "DEX 未啟用（需合約層）")
		return
	}
	q := r.URL.Query()
	am, ok := new(big.Int).SetString(q.Get("amount"), 10)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid amount")
		return
	}
	tOut, out, err := s.node.dex.Quote(q.Get("pool"), q.Get("token"), am)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "token_out": tOut, "amount_out": out.String(),
	})
}

// pendingTxCount 回傳 from 在 mempool 中尚未入塊的官方代簽交易數。
func (n *Node) pendingTxCount(from string) int64 {
	n.txNonceMu.Lock()
	defer n.txNonceMu.Unlock()
	return n.txNoncePending[from]
}

// pendingTxInc 佔用一個 pending nonce（AddMempoolTx 成功後）。
func (n *Node) pendingTxInc(from string) {
	n.txNonceMu.Lock()
	defer n.txNonceMu.Unlock()
	if n.txNoncePending == nil {
		n.txNoncePending = map[string]int64{}
	}
	n.txNoncePending[from]++
}

// resetPendingTx 清空全部 pending（新塊入庫後 db.GetNonce 已含所有已入塊交易）。
func (n *Node) resetPendingTx() {
	n.txNonceMu.Lock()
	defer n.txNonceMu.Unlock()
	n.txNoncePending = map[string]int64{}
}
