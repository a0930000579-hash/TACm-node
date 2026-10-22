package web

import (
	"embed"
	"io/fs"
	"math"
	"path/filepath"
	"math/big"
	"net/http"
	"os"
	"sync"
	"strconv"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"

	"tacm/internal/wallet"
)

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Server 為 TAC 鏈 Web 服務。
type Server struct {
	ds        DataSource
	tplSet    *pongo2.TemplateSet
	mux       *http.ServeMux
	community map[string]string
	// M64：全網監控探測的 seed 列表（New 時拷貝自 serverSeeds；測試可注入替身）。
	seedURLs []SeedInfo
	// M65：網路段（mainnet | testnet）——決定 seed 列表與加入指令的展示。
	network string
	// M85：WebSocket 訂閱中心與推送迴圈。
	hub    *wsHub
	wsStop chan struct{}
	// M103：節點 DApp 分離——false＝純節點模式（僅區塊同步/RPC/瀏覽器，不開錢包/交易所/社群）。
	ServeDApp bool
	// M147c：實際出塊間隔（秒）——審計期望獎勵需與出塊同源（annual_decay 依此計算）。
	blockTimeSec int64
	// M128-C：水龍頭每日配額追蹤。
	faucet *faucetQuota
	// M149q：審計快取——後台重放避免每次 /api/audit 全鏈掃描拖垮出塊與頁面載入。
	auditMu      sync.Mutex
	auditView    AuditView
	auditStarted bool
	auditStop    chan struct{}
}

// downloadsDirPath M144：官方下載中心目錄。預設 /var/tac/downloads；
// cmd/web 啟動時（web.New 之前）以 TACM_DOWNLOADS_DIR 指到 dataDir/downloads。
// 以函數每次求值：routes() 在 New 內執行、晚於 main 的 Setenv，故能取到正確路徑（不隨 cwd 漂移）。
func downloadsDirPath() string {
	if d := os.Getenv("TACM_DOWNLOADS_DIR"); d != "" {
		return d
	}
	return "/var/tac/downloads"
}

// seedDownloadAssets v69：啟動時將內嵌下載資產（install-node.sh）同步到 downloads 目錄，
// 保證下載中心始終提供與 binary 同版本的一鍵安裝腳本（每次啟動覆蓋，腳本隨版本更新）。
func seedDownloadAssets() {
	dir := downloadsDirPath()
	sub, err := fs.Sub(staticFS, "static/downloads")
	if err != nil {
		return
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, derr := fs.ReadFile(sub, e.Name())
		if derr != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dir, e.Name()), data, 0o755)
	}
}

// New 構造 Web 服務（mainnet 網段）：加載嵌入模板、註冊 filter 與路由。
func New(ds DataSource) (*Server, error) {
	return NewWithNetwork(ds, "mainnet")
}

// NewWithNetwork 構造 Web 服務（M65）：依網段（mainnet/testnet）選擇 seed 列表與展示。
func NewWithNetwork(ds DataSource, network string) (*Server, error) {
	return NewWithNetworkMode(ds, network, true, 0)
}

// NewWithNetworkMode M103：依網段與 DApp 開關構造 Web 服務。
// serveDApp=false 時為純節點模式：只掛鏈數據/瀏覽器/RPC 介面，不提供錢包、
// 交易所、挖礦、社群等 DApp 頁面（節點只做區塊數據流通）。
// blockTimeSec 為實際出塊間隔（秒）：審計以同源 emission 計算期望獎勵；0 時回退 1 秒。
func NewWithNetworkMode(ds DataSource, network string, serveDApp bool, blockTimeSec int64) (*Server, error) {
	tplRoot, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		return nil, err
	}
	tplSet := pongo2.NewSet("web", pongo2.NewFSLoader(tplRoot))
	registerFilters()

	if network == "" {
		network = "mainnet"
	}
	s := &Server{ds: ds, tplSet: tplSet, mux: http.NewServeMux(), network: network,
		hub: newWSHub(), wsStop: make(chan struct{}), ServeDApp: serveDApp,
		faucet: newFaucetQuota(), blockTimeSec: blockTimeSec}
	s.seedURLs = append([]SeedInfo(nil), s.serverSeeds()...)
	// 社群連結（env 配置：TAC_COMMUNITY_TELEGRAM / TAC_COMMUNITY_X / TAC_COMMUNITY_DISCORD / TAC_COMMUNITY_SITE）。
	s.community = map[string]string{
		"telegram": os.Getenv("TAC_COMMUNITY_TELEGRAM"),
		"x":        os.Getenv("TAC_COMMUNITY_X"),
		"discord":  os.Getenv("TAC_COMMUNITY_DISCORD"),
		"site":     os.Getenv("TAC_COMMUNITY_SITE"),
	}
	s.routes()
	s.auditStop = make(chan struct{})
	go s.auditLoop()
	return s, nil
}

// formatFloat8 全站顯示格式化（M77）：小數點最多 8 位、去尾零；極小值用科學記號
// 精簡（例如 5.86181640625e-7 → 0.00000059），避免長小數破版。
func formatFloat8(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	if f == 0 {
		return "0"
	}
	var out string
	if math.Abs(f) >= 1e-8 {
		out = strconv.FormatFloat(f, 'f', 8, 64)
	} else {
		out = strconv.FormatFloat(f, 'e', 6, 64)
	}
	if strings.Contains(out, ".") {
		out = strings.TrimRight(out, "0")
		out = strings.TrimRight(out, ".")
	}
	return out
}

func registerFilters() {
	fns := map[string]pongo2.FilterFunction{
		"txkind": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			memo := in.String()
			kind := "transfer"
			switch {
			case strings.HasPrefix(memo, "coinbase:miner"):
				kind = "mining"
			case strings.HasPrefix(memo, "coinbase:pool"):
				kind = "pool"
			case strings.HasPrefix(memo, "coinbase:node"):
				kind = "node"
			case strings.HasPrefix(memo, "hb:"):
				kind = "heartbeat"
			case strings.HasPrefix(memo, "vm:"):
				kind = "contract"
			case strings.HasPrefix(memo, "bridge:"):
				kind = "bridge"
			}
			return pongo2.AsValue(kind), nil
		},
		"shortaddr": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			addr := in.String()
			if len(addr) > 14 {
				return pongo2.AsValue(addr[:6] + "…" + addr[len(addr)-4:]), nil
			}
			return in, nil
		},
		"negdelta": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			neg := len(in.String()) > 0 && in.String()[0] == '-'
			return pongo2.AsValue(neg), nil
		},
		"trunc64": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			t := in.String()
			if len(t) > 64 {
				t = t[:64] + "…"
			}
			return pongo2.AsValue(t), nil
		},
		"trunc40": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			t := in.String()
			if len(t) > 40 {
				t = t[:40] + "…"
			}
			return pongo2.AsValue(t), nil
		},
		"tshort": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			ts := in.Integer()
			if ts <= 0 {
				return pongo2.AsValue("-"), nil
			}
			return pongo2.AsValue(time.Unix(int64(ts), 0).Format("01-02 15:04:05")), nil
		},
		"timefmt": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			ts := in.Integer()
			if ts <= 0 {
				return pongo2.AsValue("-"), nil
			}
			return pongo2.AsValue(time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")), nil
		},
		// M62：鏈上金額（raw 最小單位）→ 十進制（如 1000000000000000000 → 1）。
		"fmtamt": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			v, ok := new(big.Int).SetString(in.String(), 10)
			if !ok {
				return in, nil
			}
			amt := wallet.FormatAmountBig(v)
			return pongo2.AsValue(formatFloat8(amt)), nil
		},
		// M77：全站小數點顯示統一 ≤8 位（防破版）；極小值用科學記號精簡。
		"fee8": func(in *pongo2.Value, _ *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			return pongo2.AsValue(formatFloat8(in.String())), nil
		},
	}
	for name, fn := range fns {
		if pongo2.FilterExists(name) {
			_ = pongo2.ReplaceFilter(name, fn)
		} else {
			_ = pongo2.RegisterFilter(name, fn)
		}
	}
}

func (s *Server) routes() {
	// M144：官方下載中心——節點程式發布（tacnode-package.zip）。核心路由，
	// 純節點模式（--serve-dapp=false）與完整模式都掛載；目錄由
	// cmd/web 以 TACM_DOWNLOADS_DIR 指向 dataDir/downloads（絕對路徑，不隨 cwd 漂移）。
	os.MkdirAll(downloadsDirPath(), 0o755)
	seedDownloadAssets()
	s.mux.Handle("GET /downloads/", http.HandlerFunc(s.downloadsHandler))
	// M148：節點架設者面板——節點管理功能（綁定收款地址／節點獎勵查詢／提領），
	// 非 DApp：純節點模式與完整模式都掛載，架設者隨時可管理自己的節點。
	s.mux.HandleFunc("GET /node-panel", s.handleNodePanel)
	s.mux.HandleFunc("GET /network-health", s.handleNetworkHealth)
	s.mux.HandleFunc("GET /api/node/panel", s.handleNodePanelAPI)
	s.mux.HandleFunc("POST /api/node/setup", s.handleNodeSetupAPI)
	s.mux.HandleFunc("POST /api/node/bind", s.handleNodeBindAPI)
		s.mux.HandleFunc("POST /api/node/withdraw", s.handleNodeWithdrawAPI)
	s.mux.HandleFunc("POST /api/node/stake", s.handleNodeStakeAPI)
	s.mux.HandleFunc("POST /api/node/bootstrap", s.handleNodeBootstrapAPI)
	s.mux.HandleFunc("POST /api/node/register", s.handleNodeRegisterAPI) // M149m-9 PoT 節點註冊
	// M103：純節點模式（--serve-dapp=false）——只暴露鏈數據與節點管理介面，
	// 不掛載錢包/交易所/挖礦/社群/治理等整套 DApp（節點只做區塊數據流通）。
	if !s.ServeDApp {
		s.routesNodeOnly()
		return
	}
	// M85：以太坊式 WebSocket 訂閱（eth_subscribe / eth_unsubscribe）。
		s.mux.HandleFunc("GET /api/snapshot", s.handleSnapshotAPI)
		s.mux.HandleFunc("GET /api/snapshot/info", s.handleSnapshotInfo)
	s.mux.HandleFunc("GET /ws", s.handleWS)
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /app", s.handleApp)
	// M128-C：水龍頭（開發者測試幣，每日限額）。
	s.mux.HandleFunc("GET /faucet", s.handleFaucet)
	// M136-C：開發者簽名工具台（personal_sign / EIP-712 / ecRecover）。
	s.mux.HandleFunc("GET /sign", s.handleSignTool)
	// v131：ZK 工具台（批次證明/承諾/Nullifier）＋VRF 可驗證隨機數。
	s.mux.HandleFunc("GET /zk", s.handleZkTool)
	s.mux.HandleFunc("GET /contracts", s.handleContracts)
	s.mux.HandleFunc("POST /api/faucet/claim", s.handleFaucetClaimAPI)
	// M62：公開區塊瀏覽器（公鏈門面）——鏈統計＋搜尋＋最新區塊/交易。
	s.mux.HandleFunc("GET /explorer", s.handleExplorer)
	s.mux.HandleFunc("GET /api/search", s.handleSearchAPI)
	s.mux.HandleFunc("GET /api/explorer/stats", s.handleExplorerStatsAPI)
	// M63：節點加入指引＋公開 seed 列表（去中心化多節點入口）。
	s.mux.HandleFunc("GET /join", s.handleJoin)
	s.mux.HandleFunc("GET /node", s.handleNode) // M86：節點獨立品牌前端說明
	s.mux.HandleFunc("GET /api/peers", s.handlePeersAPI)
	// M64：節點健康度監控（全網狀態統一視界）。
	s.mux.HandleFunc("GET /network", s.handleNetwork)
	s.mux.HandleFunc("GET /api/network", s.handleNetworkAPI)
	// M66：鏈上審計 JSON——區塊頭＋完整交易（含 coinbase 明細），供 tacctl audit 驗證
	// 「每塊 coinbase 總額 == BlockReward」；/block/{height} 仍為 HTML 瀏覽器頁。
	s.mux.HandleFunc("GET /api/block/{height}", s.handleBlockAPI)
	s.mux.HandleFunc("GET /block/{height}", s.handleBlock)
	// M67：公開鏈上審計——任何入口 /api/audit 即可驗證「鏈上供應與獎勵一致」。
	s.mux.HandleFunc("GET /api/audit", s.handleAuditAPI)
	s.mux.HandleFunc("GET /address/{address}", s.handleAddress)
	s.mux.HandleFunc("GET /api/address/txs", s.handleAddressTxsAPI)
	s.mux.HandleFunc("GET /tx/{hash}", s.handleTx)
	s.mux.HandleFunc("POST /api/wallet/mnemonic/generate", s.handleMnemonicGenerate)
	s.mux.HandleFunc("POST /api/wallet/mnemonic/restore", s.handleMnemonicRestore)
	s.mux.HandleFunc("GET /api/wallet/txs", s.handleWalletTxsAPI)
	s.mux.HandleFunc("GET /api/wallet/txs/csv", s.handleWalletTxsCSV)
	s.mux.HandleFunc("GET /wallet", s.handleWallet)
	s.mux.HandleFunc("GET /exchange", s.handleExchange)
	s.mux.HandleFunc("GET /exchange/{sub}", s.handleExchangePage)
	s.mux.HandleFunc("GET /dashboard", s.handleDashboard)
	s.mux.HandleFunc("GET /mining", s.handleMining)
	s.mux.HandleFunc("GET /community", s.handleCommunity)
	s.mux.HandleFunc("GET /community/videos", s.handleCommunityVideos)
	s.mux.HandleFunc("GET /community/friends", s.handleCommunityFriends)
	s.mux.HandleFunc("GET /community/notifications", s.handleCommunityNotifications)
	s.mux.HandleFunc("GET /community/profile", s.handleCommunityProfile)
	s.mux.HandleFunc("GET /community/market", s.handleCommunityMarketPage)
	s.mux.HandleFunc("GET /community/ads", s.handleCommunityAdsPage)
	s.mux.HandleFunc("GET /live", s.handleLive)
	s.mux.HandleFunc("GET /ai", s.handleAI)
	s.mux.HandleFunc("GET /governance", s.handleGovernance)
	s.mux.HandleFunc("GET /tacns", s.handleTacns)
	s.mux.HandleFunc("GET /oracle", s.handleOracle)
	s.mux.HandleFunc("GET /aa", s.handleAA)
	s.mux.HandleFunc("GET /api-docs", s.handleAPIDocs)
	s.mux.HandleFunc("GET /sdk", s.handleSdk)
	s.mux.HandleFunc("GET /stake", s.handleStake)
	s.mux.HandleFunc("GET /liquid-staking", s.handleLiquidStaking)
	s.mux.HandleFunc("GET /defi", s.handleDefi)
	s.mux.HandleFunc("GET /tiusd", s.handleTiUSD)
	s.mux.HandleFunc("GET /assets", s.handleAssets)
	s.mux.HandleFunc("GET /bridge", s.handleBridge)
	s.mux.HandleFunc("GET /tokens", s.handleTokens)
	s.mux.HandleFunc("GET /nfts", s.handleNFTs)
	s.mux.HandleFunc("GET /dex", s.handleDex)
	s.mux.HandleFunc("GET /ens", s.handleENS)
	s.mux.HandleFunc("GET /auth/login", s.handleAuthLoginPage)
	s.mux.HandleFunc("GET /auth/register", s.handleAuthRegisterPage)
	// M39：節點健康檢查與運行指標（商業營運/監控告警必備）。
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	// 白皮書（中/英）——直接服務嵌入 static 成品（TAC Ai 智能鏈，無第三方品牌）。
	s.mux.HandleFunc("GET /whitepaper", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/whitepaper_zh.html", http.StatusFound)
	})
	s.mux.HandleFunc("GET /whitepaper-en", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/whitepaper_en.html", http.StatusFound)
	})
	// M41：技術黃皮書（完整技術規格，中英雙語內容單頁）。
	s.mux.HandleFunc("GET /yellowpaper", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/yellowpaper_en.html", http.StatusFound)
	})

	staticRoot, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticRoot))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
	// M94：社群本地上傳（圖片/影片）＋上傳檔靜態服務。
	s.mux.HandleFunc("POST /api/upload", s.handleUpload)
	s.mux.Handle("GET /uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadsDir))))
}

// routesNodeOnly M103：純節點模式路由（鏈數據＋區塊瀏覽器＋RPC 相關 API）。
func (s *Server) routesNodeOnly() {
	s.mux.HandleFunc("GET /ws", s.handleWS)
	// 公開區塊瀏覽器＋鏈統計＋搜尋。
	s.mux.HandleFunc("GET /explorer", s.handleExplorer)
	s.mux.HandleFunc("GET /api/search", s.handleSearchAPI)
	s.mux.HandleFunc("GET /api/explorer/stats", s.handleExplorerStatsAPI)
	// 節點加入指引＋seed 列表＋健康度。
	s.mux.HandleFunc("GET /join", s.handleJoin)
	s.mux.HandleFunc("GET /node", s.handleNode)
	s.mux.HandleFunc("GET /api/peers", s.handlePeersAPI)
	s.mux.HandleFunc("GET /network", s.handleNetwork)
	s.mux.HandleFunc("GET /api/network", s.handleNetworkAPI)
	// 區塊/交易/審計（鏈上數據，非 DApp）。
	s.mux.HandleFunc("GET /api/block/{height}", s.handleBlockAPI)
	s.mux.HandleFunc("GET /block/{height}", s.handleBlock)
	s.mux.HandleFunc("GET /api/audit", s.handleAuditAPI)
	s.mux.HandleFunc("GET /address/{address}", s.handleAddress)
	s.mux.HandleFunc("GET /api/address/txs", s.handleAddressTxsAPI)
	s.mux.HandleFunc("GET /tx/{hash}", s.handleTx)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/metrics", s.handleMetrics)
	// 白皮書/黃皮書（品牌文檔）。
	s.mux.HandleFunc("GET /whitepaper", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/whitepaper_zh.html", http.StatusFound)
	})
	s.mux.HandleFunc("GET /whitepaper-en", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/whitepaper_en.html", http.StatusFound)
	})
	s.mux.HandleFunc("GET /yellowpaper", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/yellowpaper_en.html", http.StatusFound)
	})
	staticRoot, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticRoot))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", fileServer))
}

// Handler 返回可掛載的 HTTP 處理器。
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) render(w http.ResponseWriter, name string, ctx pongo2.Context) {
	// M38：HTML 一律不緩存——避免瀏覽器/Service Worker 快取舊版頁面導致「功能沒更新」。
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	tpl, err := s.tplSet.FromCache(name)
	if err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ctx["status"] = s.ds.Status()
	ctx["community"] = s.community
	// M148：純節點模式（-serve-dapp=false）——模板精簡：頂部/底部只留節點導航，
	// 隱藏登入、工具、挖礦膠囊與整套 DApp 入口（節點是伺服器，非個人帳號介面）。
	ctx["node_only"] = !s.ServeDApp
	if err := tpl.ExecuteWriter(ctx, w); err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
	}
}

// StartWS 啟動 WebSocket 訂閱推送迴圈（cmd 啟動時調用）。
func (s *Server) StartWS() {
	go s.wsPump(s.wsStop)
	go s.marketPump(s.wsStop)
	go s.dashboardPump(s.wsStop)
}

// CloseWS 停止 WebSocket 推送迴圈。
func (s *Server) CloseWS() {
	select {
	case <-s.wsStop:
	default:
		close(s.wsStop)
	}
}
