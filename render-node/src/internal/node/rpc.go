package node

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// route 描述一條 RPC 路由；MountInto 用於將 RPC 路由掛載至外部 mux（Web/RPC 同埠模式）。
type route struct {
	pattern string
	h       http.HandlerFunc
}

// RPCServer 暴露節點的 HTTP RPC 接口。
type RPCServer struct {
	node   *Node
	mux    *http.ServeMux
	routes []route
	// M143：RPC 防灌水——per-IP 限流（寫入端點更嚴格）＋請求體積上限。
	limiter *ipLimiter
	// M128：以太式過濾器狀態（eth_newFilter / getFilterChanges / uninstallFilter）。
	filters   map[string]*ethFilter
	filtersMu sync.Mutex
}

// addRoute 同時註冊至自身 mux 並記錄，供 MountInto 回放。
// M143：統一包裝限流與 body 上限，防公開 RPC 被灌水/超大請求打掛。
func (s *RPCServer) addRoute(pattern string, h http.HandlerFunc) {
	hh := s.limitWrap(h)
	s.mux.HandleFunc(pattern, hh)
	s.routes = append(s.routes, route{pattern: pattern, h: hh})
}

// MountInto 將 RPC 路由掛載至外部 mux（Web/RPC 同埠時由 Web 單一監聽）。
// 跳過與 Web 語意重複的路由（/block/{height}、/tx/{txhash}），避免 pattern 衝突。
func (s *RPCServer) MountInto(mux *http.ServeMux) {
	skip := map[string]bool{
		"GET /block/{height}": true,
		"GET /tx/{txhash}":    true,
	}
	for _, r := range s.routes {
		if skip[r.pattern] {
			continue
		}
		mux.HandleFunc(r.pattern, r.h)
	}
}

// NewRPCServer 建立路由並註冊全部端點。
func NewRPCServer(n *Node) *RPCServer {
	mux := http.NewServeMux()
	s := &RPCServer{node: n, mux: mux, limiter: newIPLimiter(rpcRateReadLimit, rpcRateWriteLimit), filters: make(map[string]*ethFilter)}

	s.addRoute("GET /health", s.handleHealth)
	s.addRoute("GET /metrics", s.handleMetrics)
	s.addRoute("GET /api/pos/validators", s.handlePosValidators)
	s.addRoute("POST /api/pos/register", s.handlePosRegister)
	s.addRoute("POST /api/pos/stake", s.handlePosStake)
	s.addRoute("GET /api/pos/slash_evidence", s.handlePosSlashEvidence)
	s.addRoute("GET /status", s.handleStatus)
	s.addRoute("GET /api/gas", s.handleGasOracle)
	s.addRoute("GET /headers", s.handleHeaders)
	s.addRoute("GET /header/{height}", s.handleHeader)
	s.addRoute("GET /proof/{txhash}", s.handleProof)
	s.addRoute("GET /block/{height}", s.handleBlock)
	s.addRoute("GET /blocks", s.handleBlocks)
	s.addRoute("GET /tx/{txhash}", s.handleTx)
	s.addRoute("GET /account/{address}", s.handleAccount)
	s.addRoute("GET /mempool", s.handleMempool)
	s.addRoute("GET /wallet/new", s.handleWalletNew)
	s.addRoute("GET /finality", s.handleFinality)
	s.addRoute("GET /validators", s.handleValidators)
	s.addRoute("GET /finality-proof/{height}", s.handleFinalityProof)
	s.addRoute("GET /api/guest/address", s.handleGuestAddress)
	s.addRoute("POST /api/guest/restore", s.handleGuestRestore)
	s.addRoute("POST /eth", s.handleEthRPC)
	s.addRoute("POST /api/miner/register", s.handleMinerRegister)
	s.addRoute("POST /api/miner/tick", s.handleMinerTick)
	s.addRoute("POST /api/miner/start", s.handleMinerStart)
	s.addRoute("POST /api/miner/stop", s.handleMinerStop)
	s.addRoute("GET /api/miners", s.handleMiners)
	s.addRoute("GET /api/miner/earnings", s.handleMinerEarnings)
	s.addRoute("GET /api/chain/stats", s.handleChainStats)
	s.addRoute("GET /api/exchange/pool", s.handleExchangePool)
	// M21-C 社群（FB 風動態牆/市集/廣告）。
	s.addRoute("GET /api/community/feed", s.handleCommunityFeed)
	s.addRoute("POST /api/community/post", s.handleCommunityPost)
	s.addRoute("POST /api/community/post/{id}/like", s.handleCommunityLike)
	s.addRoute("POST /api/community/post/{id}/edit", s.handleCommunityPostEdit)
	s.addRoute("POST /api/community/post/{id}/delete", s.handleCommunityPostDelete)
	s.addRoute("POST /api/community/profile/meta", s.handleCommunityProfileMeta)
	s.addRoute("GET /api/community/profile/meta", s.handleCommunityProfileGet)
	s.addRoute("GET /api/community/post/{id}/comments", s.handleCommunityComments)
	s.addRoute("POST /api/community/post/{id}/comments", s.handleCommunityComment)
	s.addRoute("GET /api/community/market", s.handleCommunityMarket)
	s.addRoute("POST /api/community/market/{id}/review", s.handleCommunityReview)
	s.addRoute("GET /api/community/market/{id}/reviews", s.handleCommunityReviews)
	s.addRoute("POST /api/community/market/create", s.handleCommunityMarketCreate)
	s.addRoute("GET /api/community/market/{id}/buy-intent", s.handleCommunityBuyIntent)
	s.addRoute("POST /api/community/market/{id}/buy", s.handleCommunityBuy)
	s.addRoute("GET /api/community/ads", s.handleCommunityAds)
	s.addRoute("POST /api/community/ads/create", s.handleCommunityAdsCreate)
	s.addRoute("POST /api/community/ads/{id}/pay", s.handleCommunityAdPay)
	s.addRoute("POST /api/community/ads/{id}/impression", s.handleCommunityAdImpression)
	s.addRoute("POST /api/community/ads/{id}/click", s.handleCommunityAdClick)
	s.addRoute("POST /api/community/transfer/exchange", s.handleCommunityTransferExchange)
	s.addRoute("GET /api/community/stats", s.handleCommunityStats)
	s.addRoute("GET /api/community/config", s.handleCommunityConfig)
	s.addRoute("POST /api/live/create", s.handleLiveCreate)
	s.addRoute("GET /api/live/list", s.handleLiveList)
	s.addRoute("GET /api/live/{id}/messages", s.handleLiveMessages)
	s.addRoute("POST /api/live/{id}/message", s.handleLivePost)
	s.addRoute("POST /api/live/{id}/end", s.handleLiveEnd)
	s.addRoute("GET /api/me/assets", s.handleMeAssets)

	// M73-B 社群鏈上激勵與關注：
	s.addRoute("POST /api/community/follow", s.handleCommunityFollow)
	s.addRoute("DELETE /api/community/follow", s.handleCommunityFollow)
	s.addRoute("GET /api/community/following", s.handleCommunityFollowing)
	s.addRoute("GET /api/community/followers", s.handleCommunityFollowers)
	s.addRoute("GET /api/community/notifications", s.handleCommunityNotifications)
	s.addRoute("GET /api/community/rewards", s.handleCommunityRewards)
	s.addRoute("POST /api/community/settle", s.handleCommunitySettle)

	// M73-A AI 服務層：鏈上智能助手（規則引擎，無外部依賴）。
	s.addRoute("GET /api/ai/ask", s.handleAIAssistant)

	// M13 錢包/資產/TiUSD 業務端點。
	s.addRoute("GET /api/wallet/info", s.handleWalletInfo)
	s.addRoute("GET /api/wallet/ledger", s.handleWalletLedger)
	s.addRoute("POST /api/wallet/transfer", s.handleWalletTransfer)
	s.addRoute("POST /api/wallet/deposit", s.handleWalletDeposit)
	s.addRoute("GET /api/tiusd/summary", s.handleTiUSDSummary)
	// M31：移除公開 TiUSD 發行/銷毀——穩定幣僅由鏈上機制（供給層鑄造）產生，禁止私自鑄造。
	s.addRoute("GET /api/exchange/balances", s.handleExchangeBalances)
	s.addRoute("GET /api/exchange/orders", s.handleExchangeOrders)
	s.addRoute("POST /api/exchange/deposit", s.handleExchangeDeposit)
	s.addRoute("GET /api/exchange/book", s.handleExchangeBook)
	s.addRoute("GET /api/exchange/klines", s.handleExchangeKlines)
	s.addRoute("GET /api/exchange/mytrades", s.handleExchangeMyTrades)
	s.addRoute("POST /api/exchange/order", s.handleExchangeOrder)
	s.addRoute("POST /api/exchange/cancel", s.handleExchangeCancel)
	s.addRoute("POST /api/exchange/flash", s.handleExchangeFlash)
	s.addRoute("GET /api/exchange/markets", s.handleExchangeMarkets)
	s.addRoute("POST /api/exchange/ref/trade", s.handleExchangeRefTrade)
	s.addRoute("GET /api/exchange/ref/positions", s.handleExchangeRefPositions)
	s.addRoute("POST /api/exchange/bot/start", s.handleExchangeBotStart)
	s.addRoute("POST /api/exchange/bot/stop", s.handleExchangeBotStop)
	s.addRoute("GET /api/exchange/bots", s.handleExchangeBots)
	s.addRoute("POST /api/exchange/deposit_from_wallet", s.handleExchangeDepositFromWallet)
	s.addRoute("POST /api/exchange/withdraw_to_wallet", s.handleExchangeWithdrawToWallet)
	s.addRoute("GET /api/exchange/fee", s.handleExchangeFeeAccount)
	s.addRoute("GET /api/defi/pools", s.handleDefiPools)
	s.addRoute("GET /api/defi/my-liquidity", s.handleDefiMyLiquidity)
	s.addRoute("POST /api/defi/add-liquidity", s.handleDefiAddLiquidity)
	s.addRoute("POST /api/defi/remove-liquidity", s.handleDefiRemoveLiquidity)
	s.addRoute("POST /api/defi/claim-reward/{id}", s.handleDefiClaimReward)
	s.addRoute("GET /api/defi/lending-markets", s.handleDefiLendingMarkets)
	s.addRoute("GET /api/defi/my-deposits", s.handleDefiMyDeposits)
	s.addRoute("GET /api/defi/my-loans", s.handleDefiMyLoans)
	s.addRoute("POST /api/defi/deposit", s.handleDefiDeposit)
	s.addRoute("POST /api/defi/withdraw", s.handleDefiWithdraw)
	s.addRoute("POST /api/defi/borrow", s.handleDefiBorrow)
	s.addRoute("POST /api/defi/repay", s.handleDefiRepay)
	s.addRoute("GET /api/defi/ido-projects", s.handleDefiIDOProjects)
	s.addRoute("GET /api/defi/my-ido", s.handleDefiMyIDO)
	s.addRoute("POST /api/defi/ido-subscribe", s.handleDefiIDOSubscribe)
	s.addRoute("POST /api/defi/ido-claim/{id}", s.handleDefiIDOClaim)
	s.addRoute("GET /api/defi/vaults", s.handleDefiVaults)
	s.addRoute("GET /api/defi/my-vaults", s.handleDefiMyVaults)
	s.addRoute("POST /api/defi/vault-deposit", s.handleDefiVaultDeposit)
	s.addRoute("POST /api/defi/vault-withdraw", s.handleDefiVaultWithdraw)
	s.addRoute("POST /api/defi/vault-compound/{id}", s.handleDefiVaultCompound)
	s.addRoute("GET /api/c2c/ads", s.handleC2CAds)
	s.addRoute("POST /api/auth/register", s.handleAuthRegister)
	s.addRoute("POST /api/auth/login", s.handleAuthLogin)
	s.addRoute("POST /api/auth/logout", s.handleAuthLogout)
	s.addRoute("GET /api/auth/me", s.handleAuthMe)
	s.addRoute("POST /api/c2c/ad-create", s.handleC2CAdCreate)
	s.addRoute("GET /api/c2c/my-ads", s.handleC2CMyAds)
	s.addRoute("POST /api/c2c/ad-status", s.handleC2CAdStatus)
	s.addRoute("POST /api/c2c/order-create", s.handleC2COrderCreate)
	s.addRoute("POST /api/c2c/order-confirm", s.handleC2COrderConfirm)
	s.addRoute("POST /api/c2c/order-release", s.handleC2COrderRelease)
	s.addRoute("POST /api/c2c/order-cancel", s.handleC2COrderCancel)
	s.addRoute("POST /api/c2c/order-dispute", s.handleC2COrderDispute)
	s.addRoute("GET /api/c2c/my-orders", s.handleC2CMyOrders)
	s.addRoute("GET /api/rewardpool", s.handleRewardPool)
	s.addRoute("POST /api/rewardpool/topup", s.handleRewardPoolTopup)
	s.addRoute("POST /api/rewardpool/withdraw", s.handleRewardPoolWithdraw)
	// M77：節點獎勵提取（本機限定，節點金鑰代簽轉出節點獎勵）。
	s.addRoute("POST /api/node/reward-withdraw", s.handleNodeRewardWithdraw)

	s.addRoute("GET /contract/preview", s.handleContractPreview)
	s.addRoute("GET /contract/list", s.handleContractList)
	s.addRoute("GET /contract/get/{address}", s.handleContractGet)
	s.addRoute("GET /contract/storage/{address}/{key}", s.handleContractStorage)
	// M44：代幣發行閉環（節點官方金鑰代簽發行/呼叫＋只讀模擬查詢）。
	s.addRoute("POST /contract/deploy", s.handleContractDeploy)
	s.addRoute("POST /contract/read", s.handleContractRead)
	s.addRoute("POST /contract/verify", s.handleContractVerify)
	s.addRoute("GET /contract/abi/{address}", s.handleContractABI)
	s.addRoute("GET /contract/verified", s.handleContractVerified)
	s.addRoute("GET /contract/call/{address}", s.handleContractCallQuery)
	s.addRoute("POST /contract/call", s.handleContractCallSubmit)
	// M44：標準代幣便利查詢/轉帳（前端免組 calldata）。
	s.addRoute("GET /contract/erc20/{address}", s.handleContractERC20Info)
	s.addRoute("POST /contract/erc20/transfer", s.handleContractERC20Transfer)
	// M74-2：鏈上 NFT（ERC-721 風格）部署/查詢/mint/轉移。
	s.addRoute("POST /contract/nft/deploy", s.handleContractNFTDeploy)
	s.addRoute("GET /contract/nft/{address}", s.handleContractNFTInfo)
	s.addRoute("POST /contract/nft/mint", s.handleContractNFTMint)
	s.addRoute("POST /contract/nft/transfer", s.handleContractNFTTransfer)
	s.addRoute("POST /contract/nft/approve", s.handleContractNFTApprove)
	s.addRoute("POST /contract/nft/set-approval-for-all", s.handleContractNFTSetApprovalForAll)

	// M135：鏈上多代幣（ERC-1155）部署/查詢/mint/單筆/批次轉移/全域授權。
	s.addRoute("POST /contract/erc1155/deploy", s.handleContractErc1155Deploy)
	s.addRoute("GET /contract/erc1155/{address}", s.handleContractErc1155Info)
	s.addRoute("POST /contract/erc1155/mint", s.handleContractErc1155Mint)
	s.addRoute("POST /contract/erc1155/transfer", s.handleContractErc1155Transfer)
	s.addRoute("POST /contract/erc1155/batch-transfer", s.handleContractErc1155BatchTransfer)
	s.addRoute("POST /contract/erc1155/set-approval-for-all", s.handleContractErc1155SetApprovalForAll)

	// M45：鏈上 DEX（AMM）。
	s.addRoute("GET /dex/pools", s.handleDexPools)
	s.addRoute("POST /dex/create", s.handleDexCreate)
	s.addRoute("POST /dex/liquidity/add", s.handleDexLiquidityAdd)
	s.addRoute("POST /dex/liquidity/remove", s.handleDexLiquidityRemove)
	s.addRoute("POST /dex/swap", s.handleDexSwap)
	s.addRoute("GET /dex/quote", s.handleDexQuote)
	s.addRoute("POST /dex/stake/create", s.handleDexStakeCreate)
	s.addRoute("POST /dex/stake/fund", s.handleDexStakeFund)
	s.addRoute("POST /dex/stake", s.handleDexStake)
	s.addRoute("POST /dex/stake/unstake", s.handleDexStakeUnstake)
	s.addRoute("POST /dex/stake/claim", s.handleDexStakeClaim)
	s.addRoute("GET /dex/stake/pools", s.handleDexStakePools)
	s.addRoute("GET /dex/stake/pending", s.handleDexStakePending)

	s.addRoute("GET /api/status", s.handleAggStatus)
	s.addRoute("POST /tx/submit", s.handleTxSubmit)
	s.addRoute("POST /tx/batch", s.handleTxBatch)
	s.addRoute("POST /wallet/import", s.handleWalletImport)

	// Layer2 Optimistic Rollup
	s.addRoute("GET /l2/status", s.handleL2Status)
	s.addRoute("GET /l2/account/{address}", s.handleL2Account)
	s.addRoute("GET /l2/block/{height}", s.handleL2Block)
	s.addRoute("GET /l2/tx/{hash}", s.handleL2Tx)
	s.addRoute("POST /l2/tx", s.handleL2TxSubmit)
	s.addRoute("POST /l2/batch", s.handleL2Batch)
	s.addRoute("POST /l2/submit", s.handleL2Submit)
	s.addRoute("POST /l2/finalize", s.handleL2Finalize)
	s.addRoute("POST /l2/challenge", s.handleL2Challenge)
	s.addRoute("POST /l2/deposit", s.handleL2Deposit)
	s.addRoute("POST /l2/withdraw", s.handleL2Withdraw)

	// 跨鏈橋
	s.addRoute("GET /bridge/chains", s.handleBridgeChains)
	s.addRoute("GET /bridge/stats", s.handleBridgeStats)
	s.addRoute("POST /bridge/lock", s.handleBridgeLock)
	s.addRoute("POST /bridge/burn", s.handleBridgeBurn)
	s.addRoute("POST /bridge/confirm-lock", s.handleBridgeConfirmLock)
	s.addRoute("POST /bridge/confirm-burn", s.handleBridgeConfirmBurn)
	s.addRoute("POST /bridge/mint", s.handleBridgeMint)
	s.addRoute("POST /bridge/unlock", s.handleBridgeUnlock)
	s.addRoute("POST /bridge/status", s.handleBridgeStatus)
	s.addRoute("GET /bridge/bsc/status", s.handleBridgeBSCStatus)
	s.addRoute("POST /bridge/pause", s.handleBridgePause)
	s.addRoute("POST /bridge/resume", s.handleBridgeResume)
	s.addRoute("GET /bridge/pause_status", s.handleBridgePauseStatus)
	s.addRoute("GET /api/governance/proposals", s.handleGovernanceProposals)
	s.addRoute("GET /api/governance/params", s.handleGovernanceParams)
	s.addRoute("POST /api/governance/propose", s.handleGovernancePropose)
	s.addRoute("POST /api/governance/vote", s.handleGovernanceVote)

	return s
}

// Handler 返回可掛載的 HTTP 處理器（含 CORS）。
func (s *RPCServer) Handler() http.Handler {
	return corsMiddleware(s.mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *RPCServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "node_id": s.node.nodeID,
	})
}

func (s *RPCServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.node.GetStatus())
}

// handleGasOracle GET /api/gas — EIP-1559 建議 gas 參數（前端/錢包組費率用）。
// 回傳：目前 base_fee、建議 priority_fee、建議 max_fee、單筆交易 gas 上限。
func (s *RPCServer) handleGasOracle(w http.ResponseWriter, r *http.Request) {
	st := s.node.GetStatus()
	baseFee := st.BaseFee
	priority := 0.000000001 // 建議小費（1e-9 TACm/gas）
	gasLimit := int64(chaindb.TxGasBase)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "base_fee": baseFee, "priority_fee": priority,
		"max_fee": baseFee + priority, "gas_limit": gasLimit,
		"block_gas_limit":    st.BlockGasLimit,
		"total_fee_estimate": (baseFee + priority) * float64(gasLimit),
	})
}

func (s *RPCServer) handleHeaders(w http.ResponseWriter, r *http.Request) {
	count := 200
	if v := r.URL.Query().Get("count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			count = n
		}
	}
	res, err := s.node.GetHeadersWindow(count)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *RPCServer) handleHeader(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid height")
		return
	}
	b, err := s.node.db.GetBlock(h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, HeaderOnly(b))
}

func (s *RPCServer) handleProof(w http.ResponseWriter, r *http.Request) {
	p, err := s.node.GetTxProof(r.PathValue("txhash"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"found": false})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *RPCServer) handleBlock(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid height")
		return
	}
	b, err := s.node.GetBlockDetail(h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeErr(w, http.StatusNotFound, "block not found")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *RPCServer) handleBlocks(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	blocks, err := s.node.db.GetBlocks(limit, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": blocks})
}

func (s *RPCServer) handleTx(w http.ResponseWriter, r *http.Request) {
	tx, err := s.node.db.GetTransaction(r.PathValue("txhash"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tx == nil {
		writeErr(w, http.StatusNotFound, "tx not found")
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *RPCServer) handleAccount(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	acc, err := s.node.db.GetAccount(address)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if acc == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"address": address, "balance": "0", "nonce": 0,
		})
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (s *RPCServer) handleMempool(w http.ResponseWriter, r *http.Request) {
	txs, err := s.node.db.GetMempool(100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"txs": txs, "count": s.node.db.MempoolSize(),
	})
}

func (s *RPCServer) handleWalletNew(w http.ResponseWriter, r *http.Request) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ex, err := kp.Export()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ex)
}

func (s *RPCServer) handleTxSubmit(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	dec := json.NewDecoder(r.Body)
	dec.UseNumber() // 保留 ts/nonce 原始數字字面量，避免 float64 破壞簽名重算
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	txHash, err := s.node.SubmitTransaction(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "tx_hash": txHash, "status": "mempool",
	})
}

func (s *RPCServer) handleTxBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Transactions []map[string]any `json:"transactions"`
		Txs          []map[string]any `json:"txs"`
	}
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	txs := body.Transactions
	if txs == nil {
		txs = body.Txs
	}
	results := make([]map[string]any, 0, len(txs))
	success := 0
	// M118：批量提交走 SubmitTransactions（M117 並行驗簽），單筆失敗不影響其餘。
	hashes, errs := s.node.SubmitTransactions(txs)
	for i := range txs {
		if errs[i] != nil {
			results = append(results, map[string]any{"ok": false, "error": errs[i].Error()})
			continue
		}
		success++
		results = append(results, map[string]any{"ok": true, "tx_hash": hashes[i]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "total": len(txs), "success": success,
		"failed": len(txs) - success, "results": results,
	})
}

func (s *RPCServer) handleWalletImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PrivateKey string `json:"private_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	pk, err := hex.DecodeString(body.PrivateKey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid private key")
		return
	}
	kp, err := crypto.KeyPairFromPrivateKey(pk)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid private key")
		return
	}
	ex, err := kp.Export()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ex)
}

func (s *RPCServer) handleFinality(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.node.GetFinalityInfo())
}

func (s *RPCServer) handleValidators(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"validators": s.node.ValidatorSet().List(),
		"count":      s.node.ValidatorSet().Len(),
	})
}

func (s *RPCServer) handleFinalityProof(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid height")
		return
	}
	writeJSON(w, http.StatusOK, s.node.GetFinalityProof(h))
}
