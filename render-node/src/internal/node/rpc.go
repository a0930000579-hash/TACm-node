package node

import (
	"time"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// route 描述一條 RPC 路由；MountInto 用於將 RPC 路由掛載至外部 mux（Web/RPC 同埠模式）。
type route struct {
	pattern string
	h       http.HandlerFunc
	desc    string
}

// RPCServer 暴露節點的 HTTP RPC 接口。
type RPCServer struct {
	routeDocs []routeDoc
	node   *Node
	mux    *http.ServeMux
	routes []route
	// M143：RPC 防灌水——per-IP 限流（寫入端點更嚴格）＋請求體積上限。
	limiter *ipLimiter
	// M128：以太式過濾器狀態（eth_newFilter / getFilterChanges / uninstallFilter）。
	filters   map[string]*ethFilter
	filtersMu sync.Mutex
	// v104：dashboard 聚合快取（5s TTL，避免每次掃 3000 筆 ledger）。
	dashMu    sync.Mutex
	dashCache map[string]*dashCacheItem
	// v108：Webhook 通知去重（僅新警報推播）。
	notifyMu     sync.Mutex
	lastAlertSig string
	workerOnce   sync.Once
	// v133：personal_importRawKey 記憶體金鑰快取（不落盤；低 64 hex → priv）。
	importedKeys map[string][]byte
	importedMu   sync.Mutex
}

type dashCacheItem struct {
	data map[string]any
	at   time.Time
}

func (s *RPCServer) dashGet(key string, ttl time.Duration) map[string]any {
	s.dashMu.Lock()
	defer s.dashMu.Unlock()
	if it, ok := s.dashCache[key]; ok && time.Since(it.at) < ttl {
		return it.data
	}
	return nil
}

func (s *RPCServer) dashSet(key string, data map[string]any) {
	s.dashMu.Lock()
	defer s.dashMu.Unlock()
	if s.dashCache == nil {
		s.dashCache = make(map[string]*dashCacheItem)
	}
	s.dashCache[key] = &dashCacheItem{data: data, at: time.Now()}
}

// addRoute 同時註冊至自身 mux 並記錄，供 MountInto 回放。
// M143：統一包裝限流與 body 上限，防公開 RPC 被灌水/超大請求打掛。
func (s *RPCServer) addRoute(pattern string, h http.HandlerFunc) {
	hh := s.limitWrap(h)
	s.mux.HandleFunc(pattern, hh)
	s.routes = append(s.routes, route{pattern: pattern, h: hh})
	s.routeDocs = append(s.routeDocs, routeDoc{Pattern: pattern, Desc: ""})
}

// addRouteDoc 註冊路由並附說明（RPC 文檔頁用）。
func (s *RPCServer) addRouteDoc(pattern, desc string, h http.HandlerFunc) {
	s.addRoute(pattern, h)
	s.routeDocs = append(s.routeDocs, routeDoc{Pattern: pattern, Desc: desc})
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

// routeDoc RPC 文檔項目。
type routeDoc struct {
	Pattern string `json:"pattern"`
	Desc    string `json:"desc"`
}

// handleRPCDocs GET /api/rpc-doc — 回傳已註冊路由清單（文檔頁自動生成）。
func (s *RPCServer) handleRPCDocs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "routes": s.routeDocs})
}

// NewRPCServer 建立路由並註冊全部端點。
func NewRPCServer(n *Node) *RPCServer {
	mux := http.NewServeMux()
	s := &RPCServer{node: n, mux: mux, limiter: newIPLimiter(rpcRateReadLimit, rpcRateWriteLimit), filters: make(map[string]*ethFilter)}

	s.addRouteDoc("GET /api/rpc-doc", "RPC 路由文檔（JSON）", s.handleRPCDocs)
	s.addRouteDoc("GET /api/alerts", "最近鏈上警報事件（30 天）", s.handleAlerts)
	s.addRouteDoc("GET /api/alerts/config", "警報閾值/Webhook 配置", s.handleAlertConfig)
	s.addRouteDoc("PUT /api/alerts/config", "更新警報閾值/Webhook 配置", s.handleAlertConfig)
	s.addRoute("GET /health", s.handleHealth)
	s.addRoute("GET /metrics", s.handleMetrics)
	s.addRoute("GET /api/pos/validators", s.handlePosValidators)
	s.addRouteDoc("GET /api/validators/takeovers", "Takeover history of this node: heights this node produced while covering for missing proposers (recent 20, desc).", s.handleValidatorTakeovers)
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
	s.addRoute("GET /wallet/hd/new", s.handleWalletHDNew)
	s.addRoute("POST /wallet/hd/derive", s.handleWalletHDDerive)
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
	s.addRouteDoc("GET /api/defi/config", "DeFi 參數化治理（借貸利率，讀）", s.handleDefiConfig)
	s.addRouteDoc("PUT /api/defi/config", "DeFi 參數化治理（借貸利率，寫）", s.handleDefiConfig)
	s.addRouteDoc("GET /api/tacm/flows", "TACm 流向入口 API（14 類＋total_in/out）", s.handleTACMFlows)
	s.addRouteDoc("GET /api/gov/invite", "邀請加成參數化治理（讀）", s.handleGovInvite)
	s.addRouteDoc("PUT /api/gov/invite", "邀請加成參數化治理（寫 settings）", s.handleGovInvite)
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
	s.addRoute("GET /api/tiusd/dashboard", s.handleTiUSDDashboard)
	s.addRoute("GET /api/tacm/dashboard", s.handleTACmDashboard)
	s.addRoute("GET /api/tacm/reserve-history", s.handleTACmReserveHistory)
	s.addRoute("GET /api/tiusd/history", s.handleTiUSDHistory)
	s.addRoute("GET /api/tiusd/reserve-history", s.handleTiUSDReserveHistory)
	s.addRoute("GET /api/tiusd/positions", s.handleTiUSDPositions)
	s.addRoute("GET /api/tiusd/export.csv", s.handleAssetExport)
	s.addRoute("GET /api/tacm/export.csv", s.handleAssetExport)
	// M31：移除公開 TiUSD 發行/銷毀——穩定幣僅由鏈上機制（供給層鑄造）產生，禁止私自鑄造。
	s.addRoute("GET /api/exchange/balances", s.handleExchangeBalances)
	s.addRoute("GET /api/exchange/orders", s.handleExchangeOrders)
	s.addRoute("POST /api/exchange/deposit", s.handleExchangeDeposit)
	s.addRoute("GET /api/exchange/book", s.handleExchangeBook)
	s.addRoute("GET /api/exchange/klines", s.handleExchangeKlines)
	s.addRoute("GET /api/exchange/mytrades", s.handleExchangeMyTrades)
	s.addRoute("POST /api/exchange/stable-swap", s.handleStableSwap)
	s.addRoute("POST /api/exchange/native-swap", s.handleNativeSwap)
	s.addRoute("GET /api/exchange/stable-info", s.handleStableInfo)
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
	s.addRoute("POST /api/defi/flash_loan", s.handleDefiFlashLoan)
	s.addRoute("GET /api/defi/ido-projects", s.handleDefiIDOProjects)
	s.addRoute("POST /api/defi/cdp/open", s.handleDefiCDPOpen)
	s.addRoute("POST /api/defi/cdp/add", s.handleDefiCDPAdd)
	s.addRoute("POST /api/defi/cdp/withdraw", s.handleDefiCDPWithdraw)
	s.addRoute("POST /api/defi/cdp/repay", s.handleDefiCDPRepay)
	s.addRoute("POST /api/defi/cdp/close", s.handleDefiCDPClose)
	s.addRoute("POST /api/defi/cdp/liquidate", s.handleDefiCDPLiquidate)
	s.addRoute("GET /api/defi/cdp/my", s.handleDefiCDPMy)
	s.addRoute("GET /api/defi/cdp/market", s.handleDefiCDPMarket)
	s.addRoute("GET /api/defi/my-ido", s.handleDefiMyIDO)
	s.addRoute("POST /api/defi/ido-subscribe", s.handleDefiIDOSubscribe)
	s.addRoute("POST /api/defi/ido-claim/{id}", s.handleDefiIDOClaim)
	s.addRoute("GET /api/defi/vaults", s.handleDefiVaults)
	s.addRoute("GET /api/defi/my-vaults", s.handleDefiMyVaults)
	s.addRoute("POST /api/defi/vault-deposit", s.handleDefiVaultDeposit)
	s.addRouteDoc("GET /api/defi/vault/{id}", "DeFi vault detail (all parameters) for governance.", s.handleDefiVaultConfig)
	s.addRouteDoc("POST /api/admin/set-admin", "Promote/demote an account to bridge admin (admin-only, requires login).", s.handleAdminSetAdmin)
	s.addRouteDoc("PUT /api/defi/vault/{id}", "Governance: update vault parameters (performance_fee in [0,1], min_deposit >=0, status active|paused).", s.handleDefiVaultConfig)
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

	// P1-①：TAC-ENS 鏈上命名服務（.tac 域名）——部署/狀態/註冊/查詢/組 calldata。
	s.addRoute("POST /tacns/deploy", s.handleTACNSDeploy)
	s.addRoute("GET /tacns/status", s.handleTACNSStatus)
	s.addRoute("POST /tacns/register", s.handleTACNSRegister)
	s.addRoute("GET /tacns/query", s.handleTACNSQuery)
	s.addRoute("POST /tacns/calldata", s.handleTACNSCalldata)

	// M152：鏈上預言機——喂價快照/單價/手動覆寫/歷史。
	s.addRoute("GET /oracle/prices", s.handleOraclePrices)
	s.addRoute("GET /oracle/price", s.handleOraclePrice)
	s.addRouteDoc("GET /api/oracle/native", "原生資產喂價（TACm/TiUSD/USDT）", s.handleOracleNativePrices)
	s.addRoute("POST /oracle/feed", s.handleOracleFeed)
	s.addRoute("GET /oracle/history", s.handleOracleHistory)
	s.addRoute("POST /oracle/feeder", s.handleOracleFeeder)
	s.addRoute("POST /oracle/aggregate", s.handleOracleAggregate)
	s.addRoute("POST /aa/submit", s.handleAASubmit)
	s.addRoute("POST /aa/estimate", s.handleAAEstimate)
	s.addRoute("GET /aa/status", s.handleAAStatus)
	// M155 索引 API（Etherscan 風公共查詢面，只讀）。
	s.addRoute("GET /api/v1/stats", s.handleIndexerStats)
	s.addRoute("GET /api/v1/blocks", s.handleIndexerBlocks)
	s.addRoute("GET /api/v1/block/{id}", s.handleIndexerBlock)
	s.addRoute("GET /api/v1/tx/{hash}", s.handleIndexerTx)
	s.addRoute("GET /api/v1/txs", s.handleIndexerTxs)
	s.addRoute("GET /api/v1/address/{addr}", s.handleIndexerAddress)
	s.addRoute("GET /api/v1/delegations", s.handleIndexerDelegations)
	s.addRoute("GET /api/v1/delegations/{delegator}", s.handleIndexerDelegator)
	s.addRoute("GET /api/v1/validators", s.handleIndexerValidators)
	s.addRouteDoc("GET /api/v1/validators/{address}", "Single validator detail (stake, status, delegation totals) for explorer.", s.handleIndexerValidatorOne)

	// M156：質押委託（Staking Delegation）——委託/撤銷/狀態。
	s.addRoute("POST /stake/delegate", s.handleStakeDelegate)
	s.addRoute("POST /stake/undelegate", s.handleStakeUndelegate)
	s.addRoute("GET /stake/status", s.handleStakeStatus)

	// P1-②：TACWallet 多簽合約錢包（Safe 風格 N/M＋EIP-1271）——
	// 部署/狀態/owner 管理/門檻/提議/批准/查詢/組 calldata。
	s.addRoute("POST /tacwallet/deploy", s.handleTACWalletDeploy)
	s.addRoute("GET /tacwallet/status", s.handleTACWalletStatus)
	s.addRoute("POST /tacwallet/owner", s.handleTACWalletOwner)
	s.addRoute("POST /tacwallet/threshold", s.handleTACWalletThreshold)
	s.addRoute("POST /tacwallet/submit", s.handleTACWalletSubmit)
	s.addRoute("POST /tacwallet/approve", s.handleTACWalletApprove)
	s.addRoute("GET /tacwallet/query", s.handleTACWalletQuery)
	s.addRoute("POST /tacwallet/calldata", s.handleTACWalletCalldata)
	s.addRoute("POST /tacgov/deploy", s.handleTACGovDeploy)
	s.addRoute("GET /tacgov/status", s.handleTACGovStatus)
	s.addRoute("POST /tacgov/propose", s.handleTACGovPropose)
	s.addRoute("POST /tacgov/vote", s.handleTACGovVote)
	s.addRoute("POST /tacgov/execute", s.handleTACGovExecute)
	s.addRoute("GET /tacgov/query", s.handleTACGovQuery)
	s.addRoute("POST /tacgov/calldata", s.handleTACGovCalldata)

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
	s.addRouteDoc("GET /api/bridge/chains", "跨鏈橋分鏈狀態/pending 明細", s.handleBridgeChains)
	s.addRoute("GET /bridge/txs", s.handleBridgeTxs)
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
	s.addRoute("POST /api/liquidstake/deposit", s.handleLiquidstakeDeposit)
	s.addRoute("POST /api/liquidstake/redeem", s.handleLiquidstakeRedeem)
	s.addRoute("GET /api/liquidstake/balance", s.handleLiquidstakeBalance)
	s.addRoute("GET /api/liquidstake/stats", s.handleLiquidstakeStats)
	s.addRoute("GET /api/liquidstake/rate-history", s.handleLiquidstakeRateHistory)
	s.addRoute("POST /api/liquidstake/rewards", s.handleLiquidstakeRewards)
	s.addRoute("POST /api/liquidstake/apy", s.handleLiquidstakeSetAPY)
	s.addRoute("GET /api/governance/params", s.handleGovernanceParams)
	s.addRoute("GET /api/governance/votes", s.handleGovernanceVotes)
	s.addRoute("POST /api/governance/propose", s.handleGovernancePropose)
	s.addRoute("POST /api/governance/vote", s.handleGovernanceVote)
	s.addRoute("GET /api/governance/config", s.handleGovernanceConfig)
	// v129：治理委託投票（delegateVote）。
	s.addRoute("POST /api/governance/delegate", s.handleGovernanceDelegate)
	s.addRoute("POST /api/governance/undelegate", s.handleGovernanceUndelegate)
	s.addRoute("GET /api/governance/delegations", s.handleGovernanceDelegations)
	// v129：鏈上多簽錢包（Gnosis Safe 模式）。
	s.addRoute("POST /api/multisig/create", s.handleMultisigCreate)
	s.addRoute("GET /api/multisig/list", s.handleMultisigList)
	s.addRoute("GET /api/multisig/info", s.handleMultisigInfo)
	s.addRoute("POST /api/multisig/submit", s.handleMultisigSubmit)
	s.addRoute("POST /api/multisig/confirm", s.handleMultisigConfirm)
	s.addRoute("POST /api/multisig/execute", s.handleMultisigExecute)
	s.addRoute("POST /api/multisig/cancel", s.handleMultisigCancel)
	s.addRoute("GET /api/multisig/txs", s.handleMultisigTxs)
	s.addRoute("GET /api/multisig/confirmations", s.handleMultisigConfirmations)
	// v130：鏈上拍賣（Auction）。
	s.addRouteDoc("POST /api/auction/create", "建立拍賣（賣方/品項/資產/起標價/結束高度）", s.handleAuctionCreate)
	s.addRouteDoc("POST /api/auction/bid", "出價（需高於目前最高價）", s.handleAuctionBid)
	s.addRouteDoc("POST /api/auction/settle", "結算拍賣（到期後執行，最高價得標）", s.handleAuctionSettle)
	s.addRouteDoc("GET /api/auction/list", "拍賣列表（?status=open|sold|unsold）", s.handleAuctionList)
	s.addRouteDoc("GET /api/auction/info", "單一拍賣詳情（?id=N）", s.handleAuctionInfo)
	s.addRouteDoc("GET /api/auction/bids", "出價清單（?auction_id=N）", s.handleAuctionBids)
	// v130：ZK 證明（批次承諾/包含證明/隱私承諾/防雙花）。
	s.addRouteDoc("POST /api/zk/prove", "產生 ZK-Rollup 批次證明（承諾＋包含證明＋可選簽名）", s.handleZkProve)
	s.addRouteDoc("POST /api/zk/verify", "驗證批次證明＋淨額檢查", s.handleZkVerify)
	s.addRouteDoc("POST /api/zk/commit", "產生隱私承諾與 Nullifier（防雙花）", s.handleZkCommit)
	s.addRouteDoc("GET /api/zk/status", "ZK 原語能力清單", s.handleZkStatus)
	// v131：ECVRF 可驗證隨機數（公平抽籤/節點選舉）。
	s.addRouteDoc("POST /api/vrf/keygen", "產生 ECVRF 密鑰對", s.handleVrfKeygen)
	s.addRouteDoc("POST /api/vrf/prove", "以私鑰對訊息產生 VRF 證明與隨機輸出", s.handleVrfProve)
	s.addRouteDoc("POST /api/vrf/verify", "以公鑰驗證 VRF 證明", s.handleVrfVerify)
	s.addRouteDoc("GET /api/vrf/status", "VRF 方案能力清單", s.handleVrfStatus)

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

// handleWalletHDNew GET /wallet/hd/new — v58：標準 BIP-39 助記詞＋BIP-44 首帳戶
// （m/44'/60'/0'/0/0，MetaMask 等主流錢包可直接導入）。
func (s *RPCServer) handleWalletHDNew(w http.ResponseWriter, r *http.Request) {
	mnemonic, err := wallet.GenerateMnemonic(128)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	seed, err := crypto.MnemonicToSeed(mnemonic, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	kp, err := crypto.DeriveBIP44Key(seed, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ex, err := kp.Export()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "mnemonic": mnemonic,
		"path":    crypto.HDPathBIP44 + "/0'/0/0",
		"account": ex,
	})
}

// handleWalletHDDerive POST /wallet/hd/derive — v58：由助記詞＋路徑派生帳戶
// （預設 BIP-44：m/44'/60'/{account}'/0/{index}）。
func (s *RPCServer) handleWalletHDDerive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mnemonic   string `json:"mnemonic"`
		Passphrase string `json:"passphrase"`
		Path       string `json:"path"`
		Account    int    `json:"account"`
		Index      int    `json:"index"`
	}
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Mnemonic) == "" {
		writeErr(w, http.StatusBadRequest, "mnemonic required")
		return
	}
	seed, err := crypto.MnemonicToSeed(body.Mnemonic, body.Passphrase)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path := strings.TrimSpace(body.Path)
	if path == "" {
		if body.Account < 0 || body.Index < 0 {
			writeErr(w, http.StatusBadRequest, "account/index must be >= 0")
			return
		}
		path = fmt.Sprintf("%s/%d'/0/%d", crypto.HDPathBIP44, body.Account, body.Index)
	}
	master, err := crypto.MasterKeyFromSeed(seed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	node, err := master.ChildPath(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	kp, err := node.KeyPair()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ex, err := kp.Export()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "path": path, "account": ex,
	})
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
