// Command web 在單進程內啟動 TAC Ai 智能鏈節點（RPC + 出塊 + BFT）與 Web 區塊瀏覽。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tacm/internal/bridge"
	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/crypto"
	"tacm/internal/node"
	"tacm/internal/p2p"
	"tacm/internal/web"
)

func main() {
	nodeID := flag.String("node-id", "node1", "節點 ID")
	dataDir := flag.String("data-dir", "", "數據目錄")
	blockTime := flag.Int("block-time", 1, "目標出塊間隔（秒）")
	difficulty := flag.Int("difficulty", 1, "基礎 PoW 難度")
	rpcPort := flag.Int("rpc-port", 8332, "節點 RPC 端口")
	webPort := flag.Int("web-port", 8080, "Web 服務端口")
	serveDApp := flag.Bool("serve-dapp", true, "M103：是否提供整套 DApp 網頁（錢包/交易所/社群等）；false＝純節點模式，僅區塊同步＋RPC＋瀏覽器")
	enableL2 := flag.Bool("l2", false, "啟用 Layer2 Optimistic Rollup（含後台批量/提交）")
	enableBridge := flag.Bool("bridge", false, "啟用跨鏈橋（Lock&Mint / Burn&Unlock）")
	bridgeGuardians := flag.String("bridge-guardians", "",
		"跨鏈守衛規格：addr:pubkey 逗號分隔（配置後啟用守衛網絡：提案經 P2P 多簽聚合）")
	bridgeVault := flag.String("bridge-vault", "",
		"M150e 橋金庫 TAC 地址（資產級互通：鎖定/解鎖時真實移動 TACM；空則回退到節點地址）")
	enableBSC := flag.Bool("bsc-relay", false, "啟用 BSC 雙向中繼（TAC↔BSC 自動 mint/unlock）")
	bscRPC := flag.String("bsc-rpc", "", "BSC RPC URL（測試網或主網；發送/執行視角）")
	bscScanRPC := flag.String("bsc-scan-rpc", "", "BSC 掃描專用 RPC（eth_getLogs/eth_blockNumber；空則與 -bsc-rpc 共用）")
	bscPK := flag.String("bsc-pk", "", "BSC 側中繼私鑰（hex 32 字節）")
	bscChainID := flag.Int64("bsc-chain-id", 97, "BSC 鏈 ID（97=測試網 56=主網）")
	bscToken := flag.String("bsc-token", "", "BSC 側 TACM 映射代幣地址（0x+40hex）")
	bscLockProxy := flag.String("bsc-lock-proxy", "", "BSC 側 LockProxy 合約地址（0x+40hex）")
	bscPollMs := flag.Int("bsc-poll-ms", 10000, "BSC 中繼輪詢間隔（毫秒）")
	// M150h：多鏈擴展——Ethereum（Sepolia 11155111）/ Polygon（Amoy 80002）雙向中繼
	// （同一金庫模型；資產表各自 TAC_ETH_ASSETS / TAC_POLYGON_ASSETS 環境變數）。
	enableETH := flag.Bool("eth-relay", false, "啟用 Ethereum 雙向中繼（TAC↔ETH 自動 mint/unlock）")
	ethRPC := flag.String("eth-rpc", "", "Ethereum RPC URL（發送/執行視角）")
	ethScanRPC := flag.String("eth-scan-rpc", "", "Ethereum 掃描專用 RPC（eth_getLogs；空則與 -eth-rpc 共用）")
	ethPK := flag.String("eth-pk", "", "Ethereum 側中繼私鑰（hex 32 字節）")
	ethChainID := flag.Int64("eth-chain-id", 11155111, "Ethereum 鏈 ID（11155111=Sepolia 測試網）")
	ethToken := flag.String("eth-token", "", "Ethereum 側 TACM 映射代幣地址（0x+40hex）")
	ethLockProxy := flag.String("eth-lock-proxy", "", "Ethereum 側 LockProxy 合約地址（0x+40hex）")
	ethPollMs := flag.Int("eth-poll-ms", 10000, "Ethereum 中繼輪詢間隔（毫秒）")
	enablePolygon := flag.Bool("polygon-relay", false, "啟用 Polygon 雙向中繼（TAC↔POLYGON 自動 mint/unlock）")
	polyRPC := flag.String("polygon-rpc", "", "Polygon RPC URL（發送/執行視角）")
	polyScanRPC := flag.String("polygon-scan-rpc", "", "Polygon 掃描專用 RPC（eth_getLogs；空則與 -polygon-rpc 共用）")
	polyPK := flag.String("polygon-pk", "", "Polygon 側中繼私鑰（hex 32 字節）")
	polyChainID := flag.Int64("polygon-chain-id", 80002, "Polygon 鏈 ID（80002=Amoy 測試網）")
	polyToken := flag.String("polygon-token", "", "Polygon 側 TACM 映射代幣地址（0x+40hex）")
	polyLockProxy := flag.String("polygon-lock-proxy", "", "Polygon 側 LockProxy 合約地址（0x+40hex）")
	polyPollMs := flag.Int("polygon-poll-ms", 10000, "Polygon 中繼輪詢間隔（毫秒）")
	networkFlag := flag.String("network", "mainnet", "網路段：mainnet | testnet（testnet 自動分離資料目錄與鏈 ID）")
	p2pEnabled := flag.Bool("p2p", false, "啟用 P2P 聯網（多節點/主網模式）")
	p2pURL := flag.String("p2p-url", "", "本節點對外可達 HTTP 基址（即 web/rpc 端口，如 http://1.2.3.4:8080；P2P 端點自動位於 /p2p/ 前綴）")
	p2pSeed := flag.String("p2p-seed", "", "初始引導節點 HTTP 基址（逗號分隔，如 http://1.2.3.4:8080）")
	p2pFollower := flag.Bool("p2p-follower", false, "以驗證節點模式啟動（不主動出塊，同步並驗證區塊、參與 BFT 投票）")
	flag.Parse()

	cfg := config.Default()
	cfg.RPCPort = *rpcPort
	cfg.BlockTime = *blockTime
	cfg.Network = strings.ToLower(*networkFlag)
	cfg.EnableL2 = *enableL2
	cfg.EnableBridge = *enableBridge
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	} else if env := os.Getenv("DATA_DIR"); env != "" {
		// 容器/平台部署標準：DATA_DIR 環境變數優先於預設目錄。
		cfg.DataDir = env
	} else if cfg.Network == "testnet" {
		cfg.DataDir = "./tac_data_testnet/" + *nodeID
	} else {
		cfg.DataDir = "./tac_data/" + *nodeID
	}
	if cfg.Network == "testnet" && strings.HasPrefix(cfg.ChainID, "tacm-mainnet") {
		cfg.ChainID = "tacm-testnet-1"
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("配置校驗失敗: %v", err)
	}

	n, err := node.New(cfg, *nodeID, *difficulty)
	if err != nil {
		log.Fatalf("創建節點失敗: %v", err)
	}
	// M149m-7：錨點也發節點在線心跳（PoT 節點獎勵：在線節點集含出塊節點）。
	n.StartNodeHeartbeat(60*time.Second, nil)

	// 跨鏈橋：單進程亦可啟用守衛網絡（多簽聚合）或本地橋。
	if *enableBridge {
		if *bridgeGuardians != "" {
			guardians, gerr := node.ParseGuardians(*bridgeGuardians)
			if gerr != nil {
				log.Fatalf("解析跨鏈守衛失敗: %v", gerr)
			}
			if aerr := n.AttachBridgeNetwork(guardians); aerr != nil {
				log.Fatalf("接入跨鏈守衛網絡失敗: %v", aerr)
			}
			log.Printf("[bridge] 跨鏈守衛網絡已啟用（守衛 %d 個）", len(guardians))
		} else if err := n.AttachBridge(); err != nil {
			log.Fatalf("啟用跨鏈橋失敗: %v", err)
		}
		// M150e：資產級互通——橋掛載 TAC 側資產結算器（金庫模型）。
		vaultAddr := *bridgeVault
		if vaultAddr == "" {
			vaultAddr = node.DefaultBridgeVaultAddr()
		}
		if aerr := n.AttachBridgeAssetSettler(vaultAddr); aerr != nil {
			log.Fatalf("掛載橋資產結算器失敗: %v", aerr)
		}
		log.Printf("[bridge] 橋資產結算器已掛載（金庫=%s）", vaultAddr)
	}
	// 多鏈雙向中繼（M150h：bsc/ethereum/polygon 各自啟用；需 -bridge；僅配置 RPC/合約時才啟動）。
	startChainRelay := func(name string, enable bool, rpc, scanRPC, pk string, chainID int64, token, lockProxy string, pollMs int) {
		if !enable {
			return
		}
		if !*enableBridge {
			log.Fatalf("%s 中繼需同時指定 -bridge", name)
		}
		cfg := bridge.DefaultRelayerConfig()
		cfg.ChainName = name
		cfg.AssetsEnv = "TAC_" + strings.ToUpper(name) + "_ASSETS"
		cfg.BSCTestRPC = rpc
		cfg.BSCTestScanRPC = scanRPC
		cfg.BSCPrivateKeyHex = pk
		cfg.BSCChainID = chainID
		cfg.BSCTokenAddr = token
		cfg.BSCLockProxyAddr = lockProxy
		cfg.PollInterval = time.Duration(pollMs) * time.Millisecond
		if err := n.StartChainRelay(name, cfg); err != nil {
			log.Fatalf("啟動 %s 中繼失敗: %v", name, err)
		}
		log.Printf("[%s-relay] %s 雙向中繼已啟動（chain=%d）", name, name, chainID)
	}
	startChainRelay("bsc", *enableBSC, *bscRPC, *bscScanRPC, *bscPK, *bscChainID, *bscToken, *bscLockProxy, *bscPollMs)
	startChainRelay("ethereum", *enableETH, *ethRPC, *ethScanRPC, *ethPK, *ethChainID, *ethToken, *ethLockProxy, *ethPollMs)
	startChainRelay("polygon", *enablePolygon, *polyRPC, *polyScanRPC, *polyPK, *polyChainID, *polyToken, *polyLockProxy, *polyPollMs)

	// 數據庫為空時生成創世區塊。
	cnt, err := n.DB().GetBlockCount()
	if err != nil {
		log.Fatalf("讀取區塊數失敗: %v", err)
	}
	if cnt == 0 {
		emptyRoot, err := crypto.MerkleRootStrings(nil)
		if err != nil {
			log.Fatalf("計算空 Merkle 根失敗: %v", err)
		}
		// M73-B：社群基金創世分配 100,000 TACm（地址＝節點派生金鑰，
		// 用於每 60 塊一期的鏈上社群互動獎勵結算）。
		poolAddr, perr := n.CommunityPoolAddress()
		if perr != nil {
			log.Fatalf("計算社群基金地址失敗: %v", perr)
		}
		genesis := &chaindb.Block{
			Height: 0, Hash: strings.Repeat("0", 64),
			MerkleRoot: emptyRoot, Proposer: "genesis", Ts: time.Now().Unix(),
		}
		genesisTxs := []chaindb.Transaction{{
			TxHash:   "genesis-community-pool",
			ToAddr:   poolAddr,
			Amount:   "100000",
			Fee:      "0",
			Ts:       time.Now().Unix(),
			Memo:     "genesis:community_pool",
			Status:   "confirmed",
		}}
		if err := n.DB().InsertBlock(genesis, genesisTxs); err != nil {
			log.Fatalf("寫入創世塊失敗: %v", err)
		}
		log.Printf("[genesis] 創世區塊已生成（社群基金 %s = 100000 TACm）", poolAddr)
	}

	// 節點 RPC。
	rpcServer := node.NewRPCServer(n)
	rpcHandler := rpcServer.Handler()
	var rpcSrv *http.Server

	// P2P 聯網（可選）：錨點節點提供 seed，其餘節點以 -p2p-seed 連入；
	// follower 模式不主動 PoW 出塊，經 P2P 同步區塊並參與 BFT 投票。
	var p2pNet *p2p.Network
	if *p2pEnabled {
		if *p2pURL == "" {
			// M148：對外位址自動偵測——架設者無需手動指定。
			// 依序檢查常見雲平台提供的環境變數（Render / Fly / Heroku 等），
			// 全部缺失才要求顯式提供。
			detected := ""
			for _, env := range []string{"RENDER_EXTERNAL_URL", "FLY_PUBLIC_URL", "HEROKU_URL", "KOYEB_URL"} {
				if v := strings.TrimSpace(os.Getenv(env)); v != "" {
					detected = strings.TrimSuffix(v, "/")
					break
				}
			}
			if detected == "" {
				log.Fatalf("啟用 -p2p 需提供 -p2p-url，或設定平台環境變數（如 RENDER_EXTERNAL_URL）由節點自動偵測")
			}
			*p2pURL = detected
			log.Printf("[p2p] 自動偵測對外位址：%s", detected)
		}
		seeds := []string{}
		for _, s0 := range strings.Split(*p2pSeed, ",") {
			if s0 = strings.TrimSpace(s0); s0 != "" {
				seeds = append(seeds, s0)
			}
		}
		netw, err := n.AttachP2P(*p2pURL, seeds)
		if err != nil {
			log.Fatalf("掛載 P2P 網絡失敗: %v", err)
		}
		p2pNet = netw
	}

	// Web 區塊瀏覽。
	// M144：官方下載中心——downloads 目錄固定掛在 dataDir 下（與鏈數據一起備份/遷移）。
	if cfg.DataDir != "" {
		os.Setenv("TACM_DOWNLOADS_DIR", filepath.Join(cfg.DataDir, "downloads"))
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, "downloads"), 0o755); err != nil {
			log.Printf("建立下載目錄失敗（忽略）: %v", err)
		}
	}
	wserver, err := web.NewWithNetworkMode(web.NewNodeDataSource(n), cfg.Network, *serveDApp, int64(cfg.BlockTime))
	if err != nil {
		log.Fatalf("創建 Web 服務失敗: %v", err)
	}
	// 先啟動節點共識/出塊，再暴露 Web/RPC（避免「RPC 可達但尚未出塊」）。
	if *p2pEnabled && p2pNet != nil {
		p2pNet.Start(context.Background())
		log.Printf("[p2p] P2P 已啟動：%s（seed=%d）", *p2pURL, len(strings.Split(*p2pSeed, ",")))
	}
	if *p2pFollower {
		if !*p2pEnabled {
			log.Fatalf("-p2p-follower 需與 -p2p 一起使用（驗證節點依賴 P2P 同步）")
		}
		n.StartAsFollower()
		log.Printf("[node] 節點 %s 以跟隨模式啟動，地址 %s", *nodeID, n.Address())
	} else {
		n.Start()
		log.Printf("[node] 節點 %s 已啟動，地址 %s", *nodeID, n.Address())
	}

	// M85：啟動 WebSocket 訂閱推送（eth_subscribe newHeads / newPendingTransactions）。
	wserver.StartWS()

	var webSrv *http.Server
	if *webPort == *rpcPort {
		// 同埠模式：RPC 路由掛載進 Web mux，單一監聽
		// （Render 等平台只暴露單埠；/api/*、/health、/status 皆由 Web 同源服務）。
		wmux, ok := wserver.Handler().(*http.ServeMux)
		if !ok {
			log.Fatalf("Web handler 型別不支援同埠合併")
		}
		rpcServer.MountInto(wmux)
		finalHandler := http.Handler(wmux)
		if p2pNet != nil {
			p2pHandler := p2pNet.Handler()
			finalHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/p2p/") {
					p2pHandler.ServeHTTP(w, r)
					return
				}
				wmux.ServeHTTP(w, r)
			})
		}
		rpcSrv = &http.Server{Addr: addr(*webPort), Handler: finalHandler}
		log.Printf("[web+rpc] 同埠合併模式 :%d（RPC /health /status /api/* 已掛載）", *webPort)
		go func() {
			if err := rpcSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[web+rpc] 退出: %v", err)
			}
		}()
	} else {
		if p2pNet != nil {
			baseHandler := rpcHandler
			p2pHandler := p2pNet.Handler()
			rpcHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/p2p/") {
					p2pHandler.ServeHTTP(w, r)
					return
				}
				baseHandler.ServeHTTP(w, r)
			})
		}
		rpcSrv = &http.Server{Addr: addr(*rpcPort), Handler: rpcHandler}
		go func() {
			log.Printf("[rpc] 節點 RPC: http://0.0.0.0:%d", *rpcPort)
			if err := rpcSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[rpc] 退出: %v", err)
			}
		}()
		webSrv = &http.Server{Addr: addr(*webPort), Handler: wserver.Handler()}
		go func() {
			log.Printf("[web] Web 區塊瀏覽: http://0.0.0.0:%d", *webPort)
			if err := webSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[web] 退出: %v", err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("[shutdown] 正在停止...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if rpcSrv != nil {
		_ = rpcSrv.Shutdown(ctx)
	}
	if webSrv != nil {
		_ = webSrv.Shutdown(ctx)
	}
	if p2pNet != nil {
		p2pNet.Close()
	}
	if err := n.Close(); err != nil {
		log.Printf("[shutdown] 關閉出錯: %v", err)
	}
}

func addr(port int) string { return ":" + strconv.Itoa(port) }
