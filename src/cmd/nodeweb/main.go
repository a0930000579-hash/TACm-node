// nodeweb — TAC Ai Smart Chain 節點獨立品牌入口（M86）。
//
// 與主網 tacweb（landing/錢包/交易所/挖礦等用戶功能）完全分離：
// 本可執行檔只啟動「節點 + 節點品牌儀表板」，供節點運維者查看
// 鏈狀態 / 同步進度 / P2P peers / 驗證人集 / 出塊記錄 / 節點金鑰與獎勵。
//
// 用法範例（主網跟隨節點，獨立 UI 埠 8081）：
//
//	sudo ./nodeweb -web-port 8081 -p2p-url http://<本機IP>:8081 \
//	  -p2p-seed http://2.28.201.174:8080 -p2p-follower
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/config"
	"tacm/internal/crypto"
	"tacm/internal/node"
	"tacm/internal/nodeui"
	"tacm/internal/p2p"
)


// ringLog：記憶體 log 緩衝區（保留最近 max 行），供 /logs 頁面直接檢視節點運維 log，
// 解決 Render 控制台 log 不易取得時的診斷困難（M149e）。
type ringLog struct {
	mu  sync.Mutex
	buf [][]byte
	max int
}

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := append([]byte(nil), p...)
	r.buf = append(r.buf, line)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringLog) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.buf {
		_, _ = w.Write(l)
	}
}

func main() {
	nodeID := flag.String("node-id", "", "節點 ID（留空自動生成唯一 ID，避免與其他節點撞名）")
	dataDir := flag.String("data-dir", "", "數據目錄（預設 ./tac_data/<node-id>）")
	blockTime := flag.Int("block-time", 1, "目標出塊間隔（秒）")
	difficulty := flag.Int("difficulty", 1, "基礎 PoW 難度")
	webPort := flag.Int("web-port", 8081, "節點 UI 服務端口")
	// v136：-rpc-port 相容旗標（與 web-port 同埠合併；主網 systemd ExecStart 歷史參數）。
	rpcPort := flag.Int("rpc-port", 0, "RPC 端口（0=與 web-port 同埠，同埠合併模式）")
	networkFlag := flag.String("network", "mainnet", "網路段：mainnet | testnet")
	p2pEnabled := flag.Bool("p2p", true, "啟用 P2P 聯網（主網節點必開）")
	p2pURL := flag.String("p2p-url", "", "本節點對外可達 HTTP 基址（如 http://1.2.3.4:8081）")
	p2pSeed := flag.String("p2p-seed", "http://2.28.201.174:8080", "初始引導節點 HTTP 基址（逗號分隔）")
	p2pEncrypt := flag.Bool("p2p-encrypt", true, "啟用 P2P 訊息層加密（ECDH+AES-GCM，v65 起預設開啟；對端不支援時自動降級明文）")
	p2pPlain := flag.Bool("p2p-plain", false, "強制明文 P2P（相容舊版節點；覆蓋 -p2p-encrypt）")
	p2pFollower := flag.Bool("p2p-follower", true, "以跟隨節點模式啟動（不主動 PoW 出塊，經 P2P 同步區塊並參與 BFT 投票）")
	mainnetURL := flag.String("mainnet-url", "http://2.28.201.174:8080", "開放挖礦/主網入口 URL（節點 UI 的「開啟主網」按鈕）")
	snapshotURL := flag.String("snapshot-url", "", "啟動前從該 URL 下載狀態快照並導入（秒級就緒，跳過創世重放）")
	flag.Parse()
	// M149e：log 重定向——同時輸出 stderr 與記憶體緩衝區，節點 /logs 頁面可直讀。
	rl := &ringLog{max: 800}
	log.SetOutput(io.MultiWriter(os.Stderr, rl))
	// M149c：自動唯一節點 ID（持久化）——架設者不指定時生成 node-<隨機8hex> 並存 data-dir，
	// 重啟固定、永久唯一；避免多節點都用預設名（node1/tac-node）導致錨點誤拒握手。
	if *nodeID == "" {
		idFile := filepath.Join(*dataDir, "node_id.txt")
		if b, err := os.ReadFile(idFile); err == nil && strings.TrimSpace(string(b)) != "" {
			*nodeID = strings.TrimSpace(string(b))
		} else {
			b := make([]byte, 4)
			if _, err := rand.Read(b); err == nil {
				*nodeID = fmt.Sprintf("node-%x", b)
			} else {
				*nodeID = "node-auto"
			}
			if *dataDir != "" {
				os.MkdirAll(*dataDir, 0o755)
				os.WriteFile(idFile, []byte(*nodeID), 0o600)
			}
		}
	}

	cfg := config.Default()
	cfg.RPCPort = *webPort
	if *rpcPort > 0 {
		cfg.RPCPort = *rpcPort
	}
	cfg.BlockTime = *blockTime
	cfg.Network = strings.ToLower(*networkFlag)
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	} else if env := os.Getenv("DATA_DIR"); env != "" {
		cfg.DataDir = env
	} else if cfg.Network == "testnet" {
		cfg.DataDir = "./tac_node_data_testnet/" + *nodeID
	} else {
		cfg.DataDir = "./tac_node_data/" + *nodeID
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
	// M149m-7：節點在線心跳（PoT 節點獎勵）——每 60 秒發 hb:on:0 保持鏈上在線。
	n.StartNodeHeartbeat(60*time.Second, nil)

	// M149l/M149m-5：狀態快照導入——導入後鏈庫非空，跳過創世生成與全鏈重放。
	// 優先使用 -snapshot-url；未指定時自動從 -mainnet-url 拉取（Render 等平台
	// 無需設定 Start Command 即可秒級就緒）。
	cnt, cerr := n.DB().GetBlockCount()
	if cerr != nil {
		log.Fatalf("讀取鏈庫失敗: %v", cerr)
	}
	if cnt == 0 {
		// M149r：快照多來源（seed 輪詢）——-snapshot-url / -mainnet-url 皆可逗號分隔多個
		// 來源；依序嘗試，任一來源導入成功即就緒，全失敗才回退創世重放（去中心化容錯）。
		urls, explicit := resolveSnapshotURLs(*snapshotURL, *mainnetURL)
		if len(urls) > 0 {
			if err := importNodeSnapshotAny(n, urls); err != nil {
				if explicit {
					log.Fatalf("快照導入失敗（%d 個來源均失敗）: %v", len(urls), err)
				}
				log.Printf("[snapshot] 自動快照失敗（回退創世重放）: %v", err)
			}
		}
		// 重新讀取鏈庫（快照導入後非空）。
		cnt, _ = n.DB().GetBlockCount()
	}
	// 創世區塊（與主網 cmd/web 一致）：僅在鏈庫為空時生成；
	// 含 M73-B 社群基金 100,000 TACm 創世分配（節點派生金鑰）。
	if cnt == 0 {
		emptyRoot, merr := crypto.MerkleRootStrings(nil)
		if merr != nil {
			log.Fatalf("計算空 Merkle 根失敗: %v", merr)
		}
		poolAddr, perr := n.CommunityPoolAddress()
		if perr != nil {
			log.Fatalf("計算社群基金地址失敗: %v", perr)
		}
		genesis := &chaindb.Block{
			Height: 0, Hash: strings.Repeat("0", 64),
			MerkleRoot: emptyRoot, Proposer: "genesis", Ts: time.Now().Unix(),
		}
		genesisTxs := []chaindb.Transaction{{
			TxHash: "genesis-community-pool", ToAddr: poolAddr,
			Amount: "100000", Fee: "0", Ts: time.Now().Unix(),
			Memo: "genesis:community_pool", Status: "confirmed",
		}}
		if err := n.DB().InsertBlock(genesis, genesisTxs); err != nil {
			log.Fatalf("寫入創世塊失敗: %v", err)
		}
		log.Printf("[genesis] 創世區塊已生成（社群基金 %s = 100000 TACm）", poolAddr)
	}

	// P2P 聯網：錨點節點提供 seed，其餘節點以 -p2p-seed 連入。
	// p2p-url 自動依部署位置偵測（Render/Fly/HKoyeb），架設者不需手動改。
	if *p2pURL == "" {
		for _, env := range []string{"RENDER_EXTERNAL_URL", "FLY_PUBLIC_URL", "HEROKU_URL", "KOYEB_URL"} {
			if u := os.Getenv(env); u != "" {
				*p2pURL = strings.TrimSuffix(u, "/")
				break
			}
		}
	}
	var p2pNet *p2p.Network
	if *p2pEnabled {
		if *p2pURL == "" {
			log.Fatalf("啟用 -p2p 需提供 -p2p-url（本節點 P2P 對外基址，如 http://1.2.3.4:8081）")
		}
		var seeds []string
		for _, s0 := range strings.Split(*p2pSeed, ",") {
			s0 = strings.TrimSpace(s0)
			if s0 != "" {
				seeds = append(seeds, s0)
			}
		}
		enc := *p2pEncrypt && !*p2pPlain // v65：-p2p-plain 強制明文，覆蓋預設加密
		netw, err := n.AttachP2P(*p2pURL, seeds, enc)
		if err != nil {
			log.Fatalf("P2P 初始化失敗: %v", err)
		}
		p2pNet = netw
	}

	// M149m-8：去中心化出塊——已質押的驗證人節點一律以分布式 BFT 啟動（輪值出塊），
	// 非驗證人節點維持跟隨模式（同步＋投票），保證創世節點關閉時其他驗證人仍可出塊。
	if *p2pFollower && !n.IsValidator() {
		n.StartAsFollower()
		log.Printf("[node] 節點 %s 以跟隨模式啟動（非驗證人，同步＋投票），地址 %s", *nodeID, n.Address())
	} else {
		n.StartDistributed()
		log.Printf("[node] 節點 %s 以分布式 BFT 模式啟動（驗證人輪值出塊），地址 %s", *nodeID, n.Address())
	}
	if p2pNet != nil {
		p2pNet.Start(context.Background())
	}

	// 節點品牌 UI（獨立資料夾 internal/nodeui、獨立 binary nodeweb）。
	// v71：downloads 下載中心目錄指到 dataDir/downloads（與 tacweb 一致）。
	os.Setenv("TACM_DOWNLOADS_DIR", filepath.Join(n.DataDir(), "downloads"))
	_ = os.MkdirAll(filepath.Join(n.DataDir(), "downloads"), 0o755)
	ui := nodeui.New(n, *mainnetURL, *nodeID)
	// 同埠合併：P2P（/p2p/）掛進 UI mux——單埠對外（Render 只暴露一埠）。
	// v53-A5：nodeui.Handler() 含 panic 防護（recover→500）；同埠合併用 Mux()
	// 合併路由，最終以 Handler()（含防護）掛載監聽。
	umux := ui.Mux()
	if p2pNet != nil {
		umux.Handle("/p2p/", p2pNet.Handler()) // p2p 路由已含 /p2p/ 前綴，勿再 StripPrefix（否則 301 壞掉）
	}
	umux.HandleFunc("/logs", rl.ServeHTTP) // M149e：節點 log 直讀頁面
	addr := fmt.Sprintf("0.0.0.0:%d", *webPort)
	srv := &http.Server{Addr: addr, Handler: ui.Handler()}
	go func() {
		log.Printf("[nodeweb] 節點品牌 UI 已上線: http://0.0.0.0:%d", *webPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("節點 UI 服務失敗: %v", err)
		}
	}()

	// 優雅關閉。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	if p2pNet != nil {
		p2pNet.Close()
	}
	_ = n.Close()
	log.Println("[nodeweb] 已關閉")
}

// verifySnapshotChecksum v68：驗證快照 body 的 SHA256 與導出端 header 一致。
// 不符＝傳輸損壞或被竄改，直接拒絕導入（避免導入半殘快照後分叉）。
func verifySnapshotChecksum(body []byte, wantHex string) error {
	if strings.TrimSpace(wantHex) == "" {
		return nil // 舊快照源無 checksum header → 相容跳過。
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, wantHex) {
		return fmt.Errorf("快照校驗和不符（可能傳輸損壞/被竄改）: got %s want %s", shortSum(got), shortSum(wantHex))
	}
	return nil
}

// shortSum 校驗和縮短顯示（前 16 字元＋…）。
func shortSum(s string) string {
	if len(s) > 16 {
		return s[:16] + "…"
	}
	return s
}

// importNodeSnapshot 下載狀態快照並導入（M149l：秒級就緒，取代從創世全鏈重放）。
func importNodeSnapshot(n *node.Node, url string) error {
	log.Printf("[snapshot] 下載狀態快照 %s ...", url)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下載快照失敗: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下載快照狀態 %d", resp.StatusCode)
	}
	// v68：快照 SHA256 校驗——導出端帶 X-Snapshot-SHA256 header 時，
	// 重算比對防傳輸損壞/竄改；舊快照源（無 header）相容跳過。
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("讀取快照失敗: %w", err)
	}
	want := resp.Header.Get("X-Snapshot-SHA256")
	if want != "" {
		if err := verifySnapshotChecksum(body, want); err != nil {
			return err
		}
	}
	var sd node.SnapshotData
	if err := json.Unmarshal(body, &sd); err != nil {
		return fmt.Errorf("解析快照失敗: %w", err)
	}
	if err := n.ImportSnapshot(&sd); err != nil {
		return err
	}
	log.Printf("[snapshot] 導入完成：鏈高 %d、帳戶 %d，直接從快照高度接新塊", sd.Height, len(sd.Accounts))
	return nil
}

// resolveSnapshotURLs 解析快照來源列表（M149r）：-snapshot-url 優先（逗號分隔），
// 未指定時自動從 -mainnet-url 逗號分隔列表展開；每個來源自動補 /api/snapshot 端點
// （允許只填主機地址）。回傳 (來源列表, 是否為用戶顯式指定)。
func resolveSnapshotURLs(snapshotURL, mainnetURL string) ([]string, bool) {
	raw := snapshotURL
	explicit := strings.TrimSpace(snapshotURL) != ""
	if raw == "" {
		raw = mainnetURL
	}
	var urls []string
	for _, u0 := range strings.Split(raw, ",") {
		u := strings.TrimSpace(u0)
		if u == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(u), "/api/snapshot") {
			u = strings.TrimRight(u, "/") + "/api/snapshot"
		}
		urls = append(urls, u)
	}
	return urls, explicit
}

// importNodeSnapshotAny 依序嘗試多個快照來源（seed 輪詢）：任一來源導入成功即返回
// nil；全部失敗回傳最後一個錯誤（來源越多容錯越高，單點掛掉自動換下一個）。
func importNodeSnapshotAny(n *node.Node, urls []string) error {
	var lastErr error
	for _, u := range urls {
		log.Printf("[snapshot] 嘗試快照來源 %d/%d：%s", indexOf(urls, u)+1, len(urls), u)
		if err := importNodeSnapshot(n, u); err != nil {
			lastErr = err
			log.Printf("[snapshot] 來源 %s 失敗：%v（輪詢下一個）", u, err)
			continue
		}
		return nil
	}
	return lastErr
}

// indexOf 回傳元素在切片中的索引（用於顯示「第幾個來源」）。
func indexOf(urls []string, target string) int {
	for i, u := range urls {
		if u == target {
			return i
		}
	}
	return 0
}
