// Command node 啟動一個 TAC Ai 智能鏈節點：初始化創世、啟動 HTTP RPC 與出塊循環。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/crypto"
	"tacm/internal/node"
)

func main() {
	nodeID := flag.String("node-id", "node1", "節點 ID")
	port := flag.Int("port", 8332, "RPC 端口")
	networkFlag := flag.String("network", "mainnet", "網路段：mainnet | testnet（testnet 自動分離資料目錄與鏈 ID）")
	dataDir := flag.String("data-dir", "", "數據目錄")
	blockTime := flag.Int("block-time", 3, "目標出塊間隔（秒）")
	difficulty := flag.Int("difficulty", 2, "基礎 PoW 難度")
	peersFlag := flag.String("peers", "", "引導節點 URL，逗號分隔")
	follower := flag.Bool("follower", false, "跟隨模式：不主動出塊，僅同步與投票")
	enableL2 := flag.Bool("l2", false, "啟用 Layer2 Optimistic Rollup（含後台批量/提交）")
	enableBridge := flag.Bool("bridge", false, "啟用跨鏈橋（Lock&Mint / Burn&Unlock）")
	bridgeGuardians := flag.String("bridge-guardians", "",
		"跨鏈守衛規格：addr:pubkey 逗號分隔（配置後啟用守衛網絡：提案經 P2P 多簽聚合）")
	genesisVals := flag.String("genesis-validators", "",
		"創世驗證人規格：nodeID:address:pubkey:power，逗號分隔")
	distributed := flag.Bool("distributed", false,
		"分佈式 BFT：僅輪值 proposer 出塊，其餘投票")
	initIdentity := flag.Bool("init-identity", false,
		"僅生成/讀取密鑰並打印身份規格後退出")
	flag.Parse()

	cfg := config.Default()
	cfg.RPCPort = *port
	cfg.BlockTime = *blockTime
	cfg.Network = strings.ToLower(*networkFlag)
	if *dataDir != "" {
		cfg.DataDir = *dataDir
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
	cfg.EnableL2 = *enableL2
	cfg.EnableBridge = *enableBridge

	n, err := node.New(cfg, *nodeID, *difficulty)
	if err != nil {
		log.Fatalf("創建節點失敗: %v", err)
	}
	if *initIdentity {
		// 輸出 nodeID:address:pubkey:power，供拼裝創世驗證人規格。
		fmt.Printf("%s:%s:%s:1\n", *nodeID, n.Address(), n.IdentityPubHex())
		return
	}

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
		genesis := &chaindb.Block{
			Height:     0,
			Hash:       strings.Repeat("0", 64),
			MerkleRoot: emptyRoot,
			Proposer:   "genesis",
			Ts:         time.Now().Unix(),
		}
		if err := n.DB().InsertBlock(genesis, nil); err != nil {
			log.Fatalf("寫入創世塊失敗: %v", err)
		}
		log.Println("[genesis] 創世區塊已生成")
	}

	if *genesisVals != "" {
		if err := n.ConfigureGenesisValidators(*genesisVals); err != nil {
			log.Fatalf("配置創世驗證人失敗: %v", err)
		}
	}

	rpc := node.NewRPCServer(n)

	// 解析引導節點並接入 P2P 網絡。
	var bootstrap []string
	if *peersFlag != "" {
		for _, p := range strings.Split(*peersFlag, ",") {
			if p = strings.TrimSpace(p); p != "" {
				bootstrap = append(bootstrap, p)
			}
		}
	}
	selfURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
	p2pNet, err := n.AttachP2P(selfURL, bootstrap)
	if err != nil {
		log.Fatalf("接入 P2P 網絡失敗: %v", err)
	}

	// 跨鏈守衛網絡：配置守衛列表後，跨鏈提案經 P2P 傳播並由守衛多簽聚合。
	if *enableBridge && *bridgeGuardians != "" {
		guardians, err := node.ParseGuardians(*bridgeGuardians)
		if err != nil {
			log.Fatalf("解析跨鏈守衛失敗: %v", err)
		}
		if err := n.AttachBridgeNetwork(guardians); err != nil {
			log.Fatalf("接入跨鏈守衛網絡失敗: %v", err)
		}
		log.Printf("[bridge] 跨鏈守衛網絡已啟用（守衛 %d 個）", len(guardians))
	} else if *enableBridge {
		if err := n.AttachBridge(); err != nil {
			log.Fatalf("啟用跨鏈橋失敗: %v", err)
		}
	}

	root := http.NewServeMux()
	root.Handle("/p2p/", p2pNet.Handler())
	root.Handle("/", rpc.Handler())
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: root,
	}
	go func() {
		log.Printf("[rpc] RPC/P2P 服務已啟動: http://0.0.0.0:%d", *port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[rpc] 服務退出: %v", err)
		}
	}()

	p2pNet.Start(context.Background())
	switch {
	case *distributed:
		n.StartDistributed()
	case *follower:
		n.StartAsFollower()
	default:
		n.Start()
	}
	log.Printf("[node] 節點 %s 已啟動，地址 %s，鏈頂 %d，跟隨模式=%v",
		*nodeID, n.Address(), n.DB().GetTipHeight(), *follower)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("[shutdown] 正在停止節點...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	p2pNet.Close()
	if err := n.Close(); err != nil {
		log.Printf("[shutdown] 關閉節點出錯: %v", err)
	}
}
