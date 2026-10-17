package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flosch/pongo2/v6"

	"tacm/internal/chaindb"
	"tacm/internal/wallet"
)

// writeJSON 寫出 JSON 回應（M39 健康/指標 API 共用）。
func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	blocks, err := s.ds.Blocks(10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	txs, err := s.ds.RecentTransactions(10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "landing.html", pongo2.Context{
		"blocks": blocks, "txs": txs,
		"chain": s.ds.ChainStats(), "self": s.ds.Status(),
	})
}

// handleApp GET /app — 系統主頁（原首頁儀表：7 狀態卡＋全鏈儀表＋最新區塊/交易）。
// M80 起 `/` 為品牌門戶（landing），系統主頁移至 /app。
func (s *Server) handleApp(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.ds.Blocks(10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	txs, err := s.ds.RecentTransactions(10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "app.html", pongo2.Context{
		"blocks": blocks, "txs": txs,
		"chain": s.ds.ChainStats(), "self": s.ds.Status(),
	})
}

// handleExplorer GET /explorer — 公開區塊瀏覽器（M62）：全鏈統計＋搜尋＋最新區塊/交易。
// 資料全部來自鏈上（Blocks/RecentTransactions/Status/ChainStats），任何節點同鏈結果一致。
func (s *Server) handleExplorer(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.ds.Blocks(15)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	txs, err := s.ds.RecentTransactions(15)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scheme := "http"
	if r.TLS != nil { scheme = "https" }
	s.render(w, "explorer.html", pongo2.Context{
		"blocks": blocks, "txs": txs, "chain": s.ds.ChainStats(),
		"self": s.ds.Status(),
		"rpcURL": scheme + "://" + r.Host,
	})
}

// handleExplorerStatsAPI GET /api/explorer/stats — M124：Explorer 首頁 10 秒輪詢即時數據源。
// 回傳鏈統計（同 handleExplorer 渲染口徑）＋最新區塊/交易，前端 JS 定時重繪，無需整頁刷新。
func (s *Server) handleExplorerStatsAPI(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.ds.Blocks(8)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	txs, err := s.ds.RecentTransactions(8)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "status": s.ds.Status(), "chain": s.ds.ChainStats(),
		"blocks": blocks, "txs": txs,
	})
}

// handleSearchAPI GET /api/search?q= — 瀏覽器搜尋（M62）：數字→區塊、tx0 地址→地址頁、
// 其餘先查交易 hash→交易頁；均未命中→type=none。前端依 type 跳轉對應詳情頁。
func (s *Server) handleSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json")
	if q == "" {
		writeJSON(w, map[string]any{"ok": false, "type": "none", "q": q})
		return
	}
	if h, err := strconv.ParseInt(q, 10, 64); err == nil && h >= 0 {
		writeJSON(w, map[string]any{"ok": true, "type": "block", "height": h})
		return
	}
	if strings.HasPrefix(q, "tx0") {
		writeJSON(w, map[string]any{"ok": true, "type": "address", "address": q})
		return
	}
	if t, err := s.ds.Transaction(q); err == nil && t != nil {
		writeJSON(w, map[string]any{"ok": true, "type": "tx", "hash": q, "block_height": t.BlockHeight})
		return
	}
	writeJSON(w, map[string]any{"ok": false, "type": "none", "q": q})
}

// handlePeersAPI GET /api/peers — 公開 seed 列表（M63/M65）：新節點加入網絡的權威入口。
// 列表來自單一權威來源（mainnet 固定編譯、testnet 由 TACM_TESTNET_SEEDS 配置），
// 同一網段內任何入口看到的一致。
func (s *Server) handlePeersAPI(w http.ResponseWriter, r *http.Request) {
	chainID := "tacm-mainnet-1"
	if s.network == "testnet" {
		chainID = "tacm-testnet-1"
	}
	writeJSON(w, map[string]any{
		"ok":       true,
		"network":  s.network,
		"chain_id": chainID,
		"seeds":    s.serverSeeds(),
	})
}

// handleJoin GET /join — 節點加入指引頁（M63/M65）：依網段展示 seed 列表、一鍵指令與節點要求。
// handleNode M86：節點獨立品牌前端說明頁（cmd/nodeweb + internal/nodeui）。
func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	s.render(w, "node.html", pongo2.Context{
		"seeds":   s.serverSeeds(),
		"network": s.network,
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	s.render(w, "join.html", pongo2.Context{
		"seeds":   s.serverSeeds(),
		"repo":    "https://github.com/a0930000579-hash/TACm-Ai_Cyprto",
		"network": s.network,
	})
}

// nodeStatusResp 對應鏈節點 HTTP /status 回應（M64 監控探測用）。
// 注意：節點 /status 的 JSON 欄位名是 block_height/effective_difficulty，
// 與 Web 內部 StatusView（height/difficulty）不同——探測外部節點必須用此協議結構。
type nodeStatusResp struct {
	NodeID              string `json:"node_id"`
	Address             string `json:"address"`
	Network             string `json:"network"`
	ChainID             string `json:"chain_id"`
	BlockHeight         int64  `json:"block_height"`
	FinalBlockHeight    int64  `json:"final_block_height"`
	MempoolSize         int    `json:"mempool_size"`
	EffectiveDifficulty int    `json:"effective_difficulty"`
	UptimeSec           int64  `json:"uptime_sec"`
	Consensus           string `json:"consensus"`
}

// probeSeed 探測單一 seed 節點（M64）：GET {seed}/status、2 秒超時。
// 並行探測由呼叫方以 Goroutine 執行（Go 慣用併發寫法），本函式只做單點探測。
// handleSnapshotAPI GET /api/snapshot — 導出狀態快照（供新節點 -snapshot-url 導入）。
func (s *Server) handleSnapshotAPI(w http.ResponseWriter, r *http.Request) {
	sd, err := s.ds.Snapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sd)
}

func probeSeed(sd SeedInfo) map[string]any {
	// M149j：timeout 10s（Render free tier 冷啟動/響應慢，2s 探測會誤判離線）；
	// 探測成功後用真實 node_id 覆蓋 seed 名（seed 列表靜態識別，真實狀態動態顯示）。
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(sd.URL + "/api/status")
	base := map[string]any{"node_id": sd.NodeID, "url": sd.URL, "role": sd.Role, "reachable": false}
	if err != nil {
		base["error"] = "unreachable"
		return base
	}
	defer resp.Body.Close()
	// M149k：彈性解碼——tacweb（錨點）的 /api/status 是 {bridge, chain:{node_id,...}} 聚合包裝，
	// nodeweb（Render）是 flat {node_id,...}；兩種都要能正確解出節點狀態。
	var raw struct {
		nodeStatusResp
		Chain *nodeStatusResp `json:"chain"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		base["error"] = "bad_status"
		return base
	}
	st := raw.nodeStatusResp
	if st.NodeID == "" && raw.Chain != nil {
		st = *raw.Chain
	}
	if st.NodeID != "" {
		base["node_id"] = st.NodeID
	}
	base["reachable"] = true
	base["status"] = st
	return base
}

// handleNetworkAPI GET /api/network — 全網狀態統一視界（M64/M65）：
// 並行探測所有公開 seed 節點的 /status，加上本節點狀態，一次回傳全網節點健康度。
// seed 列表來自單一權威來源（依網段），任何節點/入口看到的節點集合一致。
func (s *Server) handleNetworkAPI(w http.ResponseWriter, r *http.Request) {
	seeds := s.seedURLs
	results := make([]map[string]any, len(seeds))
	var wg sync.WaitGroup
	for i, sd := range seeds {
		wg.Add(1)
		go func(i int, sd SeedInfo) {
			defer wg.Done()
			results[i] = probeSeed(sd)
		}(i, sd)
	}
	// M149m-9：seed 探測總超時 12s——任一 seed 冷啟動/掛死不得拖住全網監控渲染。
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
	}
	chainID := "tacm-mainnet-1"
	if s.network == "testnet" {
		chainID = "tacm-testnet-1"
	}
	writeJSON(w, map[string]any{
		"ok":       true,
		"network":  s.network,
		"chain_id": chainID,
		"self":     s.ds.Status(),
		"seeds":    results,
	})
}

// handleNetwork GET /network — 節點健康度監控頁（M64）：頁面以 JS 定時拉取 /api/network。
func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	s.render(w, "network.html", pongo2.Context{})
}

func (s *Server) handleBlock(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		http.Error(w, "invalid height", http.StatusBadRequest)
		return
	}
	b, err := s.ds.Block(h)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if b == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "block.html", pongo2.Context{"b": b})
}

// handleBlockAPI GET /api/block/{height} — 鏈上審計 JSON（M66）：
// 回傳區塊頭與完整交易（含 coinbase 瓜分明細），供 tacctl audit 逐塊驗證
// 「coinbase 總額 == BlockReward」——任何入口看到同一份鏈上數據。
func (s *Server) handleBlockAPI(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		http.Error(w, "invalid height", http.StatusBadRequest)
		return
	}
	bd, err := s.ds.BlockDetail(h)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if bd == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, bd)
}

// handleAddressTxsAPI GET /api/address/txs?address=&offset=&limit=&dir= — M123：
// 地址交易完整分頁（SQL 索引查詢，dir=in|out 過濾）。Explorer/錢包地址頁翻頁瀏覽
// 全部歷史交易（不限 50 筆）。
func (s *Server) handleAddressTxsAPI(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	w.Header().Set("Content-Type", "application/json")
	if addr == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "missing address"})
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	dir := r.URL.Query().Get("dir")
	if dir != "in" && dir != "out" {
		dir = ""
	}
	total, err := s.ds.CountTxsByAddress(addr, dir)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	txs, err := s.ds.TxsByAddressPage(addr, offset, limit, dir)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "address": addr, "total": total,
		"offset": offset, "limit": limit, "dir": dir, "txs": txs,
	})
}

func (s *Server) handleAddress(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	a, err := s.ds.Address(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// M128-B：該地址相關代幣轉移（ERC-20/721 Transfer，作為 from/to）。
	transfers, terr := transfersForAddress(s.ds, addr, 1000)
	if terr != nil {
		transfers = []TransferView{}
	} else {
		transfers = transferViewDisplay(transfers)
	}
	s.render(w, "address.html", pongo2.Context{"a": a, "transfers": transfers})
}

func (s *Server) handleTx(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	t, err := s.ds.Transaction(hash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if t == nil {
		http.NotFound(w, r)
		return
	}
	// M124：交易頁顯示確認數＝鏈頂高度−所在區塊＋1（≥1 已確認）。
	h := s.ds.Status().Height
	confs := h - t.BlockHeight + 1
	if confs < 0 {
		confs = 0
	}
	// M124：有效 Gas Price＝手續費/實際 Gas 用量（BscScan 交易頁同款欄位）。
	gprice := ""
	if t.GasUsed > 0 && t.Fee != "" {
		if f, err := strconv.ParseFloat(t.Fee, 64); err == nil && f > 0 {
			gprice = fmt.Sprintf("%.8f", f/float64(t.GasUsed))
		}
	}
	// M128-B：代幣轉移追蹤（該交易的 ERC-20/721 Transfer 事件解析）。
	logRows, logErr := s.ds.Logs(chaindb.LogFilter{TxHash: hash, Limit: 1000})
	transfers := []TransferView{}
	decodedEvents := []DecodedEvent{}
	if logErr == nil {
		transfers = transferViewDisplay(parseTransferViews(logRows))
		// M143-D：Explorer 事件 ABI 解碼——已知事件（Transfer/Approval/1155 等）解出參數供頁面顯示。
		decodedEvents = DecodeLogEvents(logRows)
	}
	s.render(w, "tx.html", pongo2.Context{
		"t": t, "height": h, "confirmations": confs, "gas_price": gprice,
		"transfers": transfers, "decoded_events": decodedEvents,
	})
}

// handleDashboard GET /dashboard — 幣安風系統管理儀表板（聚合鏈/錢包/交易所/獎勵池/L2/橋）。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dashboard.html", pongo2.Context{
		"node": s.ds.Status(),
	})
}

// handleExchange GET /exchange — 交易所門戶（行情＋功能入口）。
func (s *Server) handleExchange(w http.ResponseWriter, r *http.Request) {
	s.render(w, "exchange_home.html", pongo2.Context{
		"exchange": map[string]any{"NodeAddress": s.ds.Status().Address},
	})
}

// handleExchangePage GET /exchange/{sub} — 獨立功能子頁（trade/assets/swap/bot/orders/pool）。
func (s *Server) handleExchangePage(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	allowed := map[string]bool{"trade": true, "assets": true, "swap": true, "bot": true, "orders": true, "pool": true}
	if !allowed[sub] {
		http.NotFound(w, r)
		return
	}
	s.render(w, "exchange.html", pongo2.Context{
		"exchange": map[string]any{"NodeAddress": s.ds.Status().Address},
		"page":     sub,
	})
}

// handleMining GET /mining — 挖礦中心：節點/出塊/PoW/coinbase 分潤/獎勵池/驗證人。
// 實時數據由前端直接讀取節點 RPC（與 exchange 頁一致：?rpc= 可覆蓋）。
func (s *Server) handleMining(w http.ResponseWriter, r *http.Request) {
	s.render(w, "mining.html", pongo2.Context{})
}

// handleCommunity GET /community — 內建社群（FB 風動態牆/市集/廣告，App 化）。
// 實時數據由前端直接讀取節點 RPC（?rpc= 可覆蓋）。
func (s *Server) handleCommunity(w http.ResponseWriter, r *http.Request) {
	s.render(w, "community.html", pongo2.Context{})
}

// handleAI GET /ai — AI 服務：鏈上智能助手（M73-A）。
// 實時回答由前端直接讀取節點 RPC（/api/ai/ask，?rpc= 可覆蓋）。
func (s *Server) handleAI(w http.ResponseWriter, r *http.Request) {
	s.render(w, "ai.html", pongo2.Context{})
}

// handleSignTool GET /sign — 開發者簽名工具台（EIP-191/EIP-712/ecRecover）。
// 後端 RPC 見 internal/node/eth_sign_rpc.go；前端直接 POST /eth。
func (s *Server) handleSignTool(w http.ResponseWriter, r *http.Request) {
	s.render(w, "sign_tool.html", pongo2.Context{})
}

// handleContracts GET /contracts — 合約驗證中心（M139）。
// 列表/read 互動由前端直接讀取節點 RPC（POST /contract/read 走 ABI 編碼）。
func (s *Server) handleContracts(w http.ResponseWriter, r *http.Request) {
	s.render(w, "contracts.html", pongo2.Context{})
}

// handleGovernance GET /governance — 鏈上治理（提案/投票/參數，App 化）。
// 實時數據由前端直接讀取節點 RPC（?rpc= 可覆蓋）。
func (s *Server) handleGovernance(w http.ResponseWriter, r *http.Request) {
	s.render(w, "governance.html", pongo2.Context{})
}

// handleDefi GET /defi — DeFi 中心（流動性挖礦＋借貸市場，App 化）。
// 實時數據由前端直接讀取節點 RPC（?rpc= 可覆蓋）。
func (s *Server) handleDefi(w http.ResponseWriter, r *http.Request) {
	s.render(w, "defi.html", pongo2.Context{})
}


// handleTokens GET /tokens — 代幣工作室（Token Studio）：鏈上標準代幣發行
// （節點官方金鑰代簽）＋列表/詳情/轉帳，App 化。實時數據由前端讀取 RPC。
func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	s.render(w, "tokens.html", pongo2.Context{})
}

// handleNFTs GET /nfts — NFT 工作室（NFT Studio）：鏈上標準 NFT（ERC-721 風格）
// 發行/mint/查詢/轉移（M74-2）。實時數據由前端讀取 RPC。
func (s *Server) handleNFTs(w http.ResponseWriter, r *http.Request) {
	s.render(w, "nfts.html", pongo2.Context{})
}

// handleDex GET /dex — 鏈上 DEX（AMM 恆定乘積）：池列表/建池/流動性/即時兌換，
// 與 Token Studio 打通（發幣→建池→交易閉環）。實時數據由前端讀取 RPC。
func (s *Server) handleDex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dex.html", pongo2.Context{})
}

// handleBridge GET /bridge — 跨鏈橋中心（M149w）：BSC 橋狀態、資產映射、
// TAC→BSC 鎖定操作與跨鏈歷史。實時數據由前端讀取同源 RPC（/bridge/*）。
func (s *Server) handleBridge(w http.ResponseWriter, r *http.Request) {
	s.render(w, "bridge.html", pongo2.Context{})
}

// handleWallet GET /wallet?address=tx0xxx — 幣安風多資產錢包頁面。
func (s *Server) handleWallet(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("address")
	if addr == "" {
		// M37：登入會員優先顯示「會員自己的鏈上錢包」；未登入訪客由前端覆寫訪客獨立地址。
		if u, err := s.ds.CurrentUser(r); err == nil && u != nil && u.WalletAddr != "" {
			addr = u.WalletAddr
		} else {
			addr = s.ds.Status().Address
		}
	}
	wv, err := s.ds.Wallet(addr)
	if err != nil {
		http.Error(w, "錢包讀取失敗: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// M37：「我的地址」渲染當前視圖地址（登入會員＝會員鏈上地址；訪客＝節點地址 fallback，由前端覆寫訪客獨立地址）。
	wv.NodeAddress = addr
	// 鏈上獎勵池餘額（coinbase 15% 挹注 + 交易所手續費結算）。
	pool := "0"
	if poolAcct, err := s.ds.Wallet(wallet.RewardPoolAddr); err == nil {
		pool = poolAcct.TACm
	}
	s.render(w, "wallet.html", pongo2.Context{
		"wallet": wv, "address": addr, "addrParam": addr, "rewardPool": pool,
	})
}

// handleAuthLoginPage 會員登入頁（幣安風；JS 呼叫 /api/auth/login）。
func (s *Server) handleAuthLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "auth.html", pongo2.Context{"activeTab": "auth"})
}

// handleAuthRegisterPage 會員註冊頁（JS 呼叫 /api/auth/register）。
func (s *Server) handleAuthRegisterPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "auth.html", pongo2.Context{"activeTab": "auth"})
}

// handleHealth GET /api/health — 節點存活探針（監控告警/負載均衡用）。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st := s.ds.Status()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"ok":                 true,
		"node_id":            st.NodeID,
		"address":            st.Address,
		"block_height":       st.Height,
		"final_block_height": st.FinalizedHeight,
		"uptime_sec":         st.UptimeSec,
		"consensus":          st.Consensus,
	})
}

// handleMetrics GET /api/metrics — 節點運行指標（區塊高度/難度/礦工/算力/供應）。
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	st := s.ds.Status()
	cs := s.ds.ChainStats()
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, map[string]any{
		"ok":               true,
		"height":           st.Height,
		"finalized_height": st.FinalizedHeight,
		"difficulty":       st.Difficulty,
		"mempool_size":     st.MempoolSize,
		"uptime_sec":       st.UptimeSec,
		"consensus":        st.Consensus,
		"online_miners":    cs.OnlineMiners,
		"network_hashrate": cs.TotalHashrate,
		"total_mined_tacm": cs.TotalMinedTacm,
		"tiusd_supply":     cs.TiUSDSupply,
		"reward_pool":      cs.RewardPool,
	})
}

// handleCommunityVideos GET /community/videos — M95：影片瀑布流（FB Reels 風）。
func (s *Server) handleCommunityVideos(w http.ResponseWriter, r *http.Request) {
	s.render(w, "videos.html", pongo2.Context{})
}

// handleCommunityFriends GET /community/friends — M95：好友管理頁（FB 風）。
func (s *Server) handleCommunityFriends(w http.ResponseWriter, r *http.Request) {
	s.render(w, "friends.html", pongo2.Context{})
}

// handleCommunityNotifications GET /community/notifications — M95：社群通知中心。
func (s *Server) handleCommunityNotifications(w http.ResponseWriter, r *http.Request) {
	s.render(w, "notifications.html", pongo2.Context{})
}

// handleCommunityProfile GET /community/profile — M95：個人主頁（鏈上數據/資產/礦機入口）。
func (s *Server) handleCommunityProfile(w http.ResponseWriter, r *http.Request) {
	s.render(w, "profile.html", pongo2.Context{})
}

// handleCommunityMarketPage GET /community/market — M97：市集獨立頁。
func (s *Server) handleCommunityMarketPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "market.html", pongo2.Context{})
}

// handleCommunityAdsPage GET /community/ads — M97：廣告投放中心（FB/YT 式）。
func (s *Server) handleCommunityAdsPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "ads.html", pongo2.Context{})
}

// handleLive GET /live — 社群直播頁（M93）。
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	s.render(w, "live.html", pongo2.Context{})
}

// M94：社群本地上傳（圖片/影片）——存到工作目錄 uploads/，回 /uploads/<name> URL。
var (
	uploadsDir  = "uploads"
	uploadExts  = map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
		".gif": "image/gif", ".webp": "image/webp",
		".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	}
	uploadMax = int64(64 << 20) // 64MB
)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(uploadMax + 1<<20); err != nil {
		http.Error(w, "upload too large or invalid: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	if header.Size > uploadMax {
		http.Error(w, "file exceeds 64MB limit", http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	ct, ok := uploadExts[ext]
	if !ok {
		http.Error(w, "unsupported file type (jpg/png/gif/webp/mp4/webm/mov)", http.StatusBadRequest)
		return
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := hex.EncodeToString(buf) + ext
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		http.Error(w, "cannot create upload dir", http.StatusInternalServerError)
		return
	}
	dst, err := os.Create(filepath.Join(uploadsDir, name))
	if err != nil {
		http.Error(w, "cannot save file", http.StatusInternalServerError)
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"url":"/uploads/%s","type":"%s","size":%d}`, name, ct, header.Size)
}
