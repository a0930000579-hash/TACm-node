// loadtest 是 TAC Ai 智能鏈的高併發壓測工具（v156 A1）。
//
// 不透過 internal/node 套件，改以 HTTP 對真實節點灌量：
//   - -bin：啟動 tacweb/nodeweb 二進位子進程（臨時 datadir、web-port==rpc-port、關 P2P），
//     就緒後讀 <datadir>/node_key.json 當 coinbase/funder；或
//   - -url：打已存在節點（搭配 -sender-key-hex 指定 funder 私鑰）。
//
// 真鏈特性（不可改核心碼）：同一 sender 爆量連送時只有貼近當下 nonce 的那筆被收，
// 故採多 sender（-senders）+ 批量端點 POST /tx/batch（同批同 sender 連續 nonce 由伺服器
// extraPending 正確計算、單一事務落池），並對 "bad nonce: expected N" 自動校正重送，
// 把錯誤率壓到接近 0。TPS 以伺服器實際入帳成功筆數/執行秒數計。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tacm/internal/crypto"
)

type config struct {
	bin          string
	url          string
	datadir      string
	port         int
	senders      int
	batchSize    int
	mode         string // batch | single
	rate         int    // tx/sec 目標；0 = 不限制（盡快灌）
	dur          time.Duration
	out          string
	senderKeyHex string // funder 私鑰（-url 模式）
	fundAmount   string
	keep         bool
}

// dbgPrint 只印前 3 筆失敗（除錯用）。
var dbgCnt int32

func dbgPrint() bool {
	return atomic.AddInt32(&dbgCnt, 1) <= 3
}

func pickFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type nodeProc struct {
	cmd     *exec.Cmd
	baseURL string
	dbPath  string
	dataDir string
}

func waitHTTP(baseURL, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := baseURL + path
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting %s", url)
}

func chainHeight(baseURL string) (int64, error) {
	resp, err := http.Get(baseURL + "/api/status")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var st struct {
		Chain struct {
			BlockHeight int64 `json:"block_height"`
		} `json:"chain"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, err
	}
	return st.Chain.BlockHeight, nil
}

func waitHeight(baseURL string, minH int64, timeout time.Duration) (int64, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h, err := chainHeight(baseURL); err == nil && h >= minH {
			return h, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0, errors.New("timeout waiting for blocks")
}

func startNode(cfg *config) (*nodeProc, error) {
	if cfg.datadir == "" {
		d, err := os.MkdirTemp("", "tac_loadtest_")
		if err != nil {
			return nil, err
		}
		cfg.datadir = d
	} else {
		_ = os.MkdirAll(cfg.datadir, 0o755)
	}
	if cfg.port == 0 {
		cfg.port = pickFreePort()
	}
	args := []string{
		"-data-dir", cfg.datadir,
		"-web-port", strconv.Itoa(cfg.port),
		"-rpc-port", strconv.Itoa(cfg.port), // web==rpc 同埠才會 MountInto 掛載 /tx/submit /tx/batch
		"-block-time", "1",
		"-difficulty", "1",
		"-p2p=false",
		"-p2p-follower=false",
	}
	cmd := exec.Command(cfg.bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("啟動節點失敗: %w", err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.port)
	if err := waitHTTP(base, "/api/status", 90*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	// 等出夠幾塊，使 coinbase(funder) 有足夠餘額資金化 N 個 sender。
	if _, err := waitHeight(base, 5, 90*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	return &nodeProc{
		cmd:     cmd,
		baseURL: base,
		dbPath:  filepath.Join(cfg.datadir, "chain.db"),
		dataDir: cfg.datadir,
	}, nil
}

func (n *nodeProc) stop() {
	if n.cmd != nil && n.cmd.Process != nil {
		_ = n.cmd.Process.Signal(os.Interrupt)
		_ = n.cmd.Wait()
	}
}

// sender 為單一簽名者，各自維護嚴格連續 nonce。
type sender struct {
	kp     *crypto.KeyPair
	addr   string
	pubHex string
	nonce  int64
}

func newSender(kp *crypto.KeyPair) *sender {
	addr, _ := kp.Address()
	return &sender{kp: kp, addr: addr, pubHex: bytesHex(kp.PublicKeyCompressed())}
}

func dbSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// postBatch POST /tx/batch，回傳 HTTP 碼、逐筆 results、原始 body。
func postBatch(baseURL string, txs []map[string]any) (int, []map[string]any, []byte, error) {
	payload := map[string]any{"transactions": txs}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(baseURL+"/tx/batch", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Results []map[string]any `json:"results"`
	}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out.Results, raw, nil
}

// postSingle POST /tx/submit。
func postSingle(baseURL string, tx map[string]any) (int, []byte, error) {
	body, _ := json.Marshal(tx)
	resp, err := http.Post(baseURL+"/tx/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

// parseExpectedFromError 從 "bad nonce: expected N, got M" 解析 N。
func parseExpectedFromError(msg string) (int64, bool) {
	idx := strings.Index(msg, "expected ")
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(msg[idx+len("expected "):])
	if i := strings.IndexAny(rest, " ,"); i >= 0 {
		rest = rest[:i]
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseExpectedNonce 從整個 HTTP 錯誤 body 解析 expected nonce。
func parseExpectedNonce(raw []byte) (int64, bool) {
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, false
	}
	return parseExpectedFromError(env.Error)
}

type batchOutcome struct {
	accepted  int64
	failed    int64
	latencyMs float64
}

// sendBatchForSender 幫單一 sender 組一批連續 nonce 交易送出，bad-nonce 自動校正重送。
// to 為固定收款方；amount 為每筆金額。回傳實際入帳成功/失敗數與往返延遲。
func sendBatchForSender(baseURL string, s *sender, to, amount string, batchSize int) batchOutcome {
	for attempt := 0; attempt < 4; attempt++ {
		txs := make([]map[string]any, 0, batchSize)
		for k := 0; k < batchSize; k++ {
			n := s.nonce + int64(k)
			tx := map[string]any{
				"from": s.addr, "to": to, "amount": amount, "fee": "0",
				"nonce": n, "ts": time.Now().Unix(), "pubkey": s.pubHex,
			}
			sig, err := crypto.SignTransaction(tx, s.kp.PrivateKey())
			if err != nil {
				continue
			}
			tx["signature"] = sig
			txs = append(txs, tx)
		}
		if len(txs) == 0 {
			return batchOutcome{}
		}
		start := time.Now()
		code, results, raw, err := postBatch(baseURL, txs)
		lat := time.Since(start).Seconds() * 1000
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if code != 200 {
			// 整批被拒；若為 nonce 漂移則校正重送；否則視為 mempool 滿等暫態背壓，
			// 短退避後重試最多 3 次再放棄（避免把瞬時背壓記成永久失敗）。
			if exp, ok := parseExpectedNonce(raw); ok {
				s.nonce = exp
				continue
			}
			if attempt < 3 {
				time.Sleep(time.Duration(100*(attempt+1)) * time.Millisecond)
				continue
			}
			if dbgPrint() {
				fmt.Printf("loadtest[fail] sender=%s nonce=%d http=%d body=%s\n",
					s.addr, s.nonce, code, strings.TrimSpace(string(raw)))
			}
			return batchOutcome{failed: int64(len(txs)), latencyMs: lat}
		}
		accepted := int64(0)
		failed := int64(0)
		var expNonce int64
		hasExp := false
		for _, r := range results {
			if ok, _ := r["ok"].(bool); ok {
				accepted++
			} else {
				failed++
				if e, ok := r["error"].(string); ok {
					if dbgPrint() {
						fmt.Printf("loadtest[txfail] sender=%s nonce=%d err=%s\n", s.addr, s.nonce, e)
					}
					if n2, ok2 := parseExpectedFromError(e); ok2 {
						expNonce, hasExp = n2, true
					}
				}
			}
		}
		// 全軍覆沒且是 nonce 漂移 → 校正重送。
		if accepted == 0 && hasExp {
			s.nonce = expNonce
			continue
		}
		// 有入帳成功 → nonce 推進 accepted（下一批緊接其後）。
		s.nonce += accepted
		return batchOutcome{accepted: accepted, failed: failed, latencyMs: lat}
	}
	return batchOutcome{failed: int64(batchSize)}
}

// fundSenders 用 funder 金鑰經 /tx/batch 把 fundAmount 轉給每個 sender，連續 nonce、
// bad-nonce 自動校正，並重試直到全部 sender 都收到資金（或嘗試用盡）。
func fundSenders(baseURL string, funder *sender, senders []*sender, amount string) error {
	funded := make(map[int]bool)
	for attempt := 0; attempt < 8; attempt++ {
		// 只對尚未收到資金的 sender 重送。
		pending := make([]*sender, 0, len(senders))
		for i, sd := range senders {
			if !funded[i] {
				pending = append(pending, sd)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		txs := make([]map[string]any, 0, len(pending))
		for i, sd := range pending {
			n := funder.nonce + int64(i)
			tx := map[string]any{
				"from": funder.addr, "to": sd.addr, "amount": amount, "fee": "0",
				"nonce": n, "ts": time.Now().Unix(), "pubkey": funder.pubHex,
			}
			sig, err := crypto.SignTransaction(tx, funder.kp.PrivateKey())
			if err != nil {
				return err
			}
			tx["signature"] = sig
			txs = append(txs, tx)
		}
		code, results, raw, err := postBatch(baseURL, txs)
		if err != nil {
			time.Sleep(150 * time.Millisecond)
			continue
		}
		if code != 200 {
			if exp, ok := parseExpectedNonce(raw); ok {
				funder.nonce = exp
				continue
			}
			time.Sleep(150 * time.Millisecond)
			continue
		}
		// 逐筆標記成功的 sender。
		for i, r := range results {
			if i < len(pending) {
				if ok, _ := r["ok"].(bool); ok {
					// 反查是哪個 sender。
					for j := range senders {
						if senders[j] == pending[i] {
							funded[j] = true
						}
					}
					funder.nonce++
				} else if e, ok := r["error"].(string); ok {
					if exp, ok2 := parseExpectedFromError(e); ok2 {
						funder.nonce = exp
					}
				}
			}
		}
	}
	unfunded := 0
	for _, ok := range funded {
		if !ok {
			unfunded++
		}
	}
	if unfunded > 0 {
		return fmt.Errorf("%d/%d 個 sender 資金失敗", unfunded, len(senders))
	}
	return nil
}

type samplePoint struct {
	T            int64  `json:"t_sec"`
	HeapAlloc    uint64 `json:"heap_alloc"`
	HeapInuse    uint64 `json:"heap_inuse"`
	NumGC        uint32 `json:"num_gc"`
	NumGoroutine int    `json:"num_goroutine"`
	DBBytes      int64  `json:"db_bytes"`
	ChainHeight  int64  `json:"chain_height"`
}

type report struct {
	StartedAt          string        `json:"started_at"`
	DurationSec        float64       `json:"duration_sec"`
	Senders            int           `json:"senders"`
	BatchSize          int           `json:"batch_size"`
	Mode               string        `json:"mode"`
	RatePerSec         int           `json:"rate_per_sec"`
	Success            int64         `json:"success"`
	Failed             int64         `json:"failed"`
	ErrorRate          float64       `json:"error_rate"`
	ActualTPS          float64       `json:"actual_tps"`
	LatencyP50Ms       float64       `json:"latency_p50_ms"`
	LatencyP95Ms       float64       `json:"latency_p95_ms"`
	LatencyP99Ms       float64       `json:"latency_p99_ms"`
	ChainDBBytesBefore int64         `json:"chain_db_bytes_before"`
	ChainDBBytesAfter  int64         `json:"chain_db_bytes_after"`
	ChainDBGrowthBytes int64         `json:"chain_db_growth_bytes"`
	FinalHeapAlloc     uint64        `json:"final_heap_alloc"`
	FinalNumGC         uint32        `json:"final_num_gc"`
	FinalNumGoroutine  int           `json:"final_num_goroutine"`
	MemSamples         []samplePoint `json:"mem_samples"`
	Funder             string        `json:"funder"`
	NodeURL            string        `json:"node_url"`
	FinalChainHeight   int64         `json:"final_chain_height"`
}

// waitForSendersFunded 等待資金交易真正入塊：記錄 h0，輪詢直到鏈高推進 minAdvance 塊，
// 確保 funder→sender 的資金 tx 已被打包入塊（chain accounts 已入帳）。
func waitForSendersFunded(baseURL string, minAdvance int64, timeout time.Duration) {
	h0, _ := chainHeight(baseURL)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h, err := chainHeight(baseURL); err == nil && h >= h0+minAdvance {
			fmt.Printf("loadtest: 資金已入塊（鏈高 %d→%d）\n", h0, h)
			return
		}
		time.Sleep(400 * time.Millisecond)
	}
	fmt.Println("loadtest: 警告：等資金入塊逾時，繼續灌量")
}

func main() {
	cfg := &config{}
	flag.StringVar(&cfg.bin, "bin", "", "tacweb/nodeweb 二進位路徑（與 -url 二選一）")
	flag.StringVar(&cfg.url, "url", "", "已存在節點 base URL（如 http://127.0.0.1:8081）")
	flag.StringVar(&cfg.datadir, "datadir", "", "子進程數據目錄（預設 mktemp）")
	flag.IntVar(&cfg.port, "port", 0, "子進程 port（0=自選空閒埠）")
	flag.IntVar(&cfg.senders, "senders", 16, "並發簽名者數量（各自獨立 nonce）")
	flag.IntVar(&cfg.batchSize, "batch-size", 200, "每批交易數（batch 模式）")
	flag.StringVar(&cfg.mode, "mode", "batch", "灌量模式：batch（/tx/batch）| single（/tx/submit）")
	flag.IntVar(&cfg.rate, "rate", 0, "目標 tx/sec（0=不限制，盡快灌）")
	flag.DurationVar(&cfg.dur, "dur", 30*time.Second, "壓測持續時間")
	flag.StringVar(&cfg.out, "out", "", "輸出機讀 JSON 報告（可選）")
	flag.StringVar(&cfg.senderKeyHex, "sender-key-hex", "", "-url 模式下 funder 私鑰 hex")
	flag.StringVar(&cfg.fundAmount, "fund-amount", "1", "每個 sender 資金數量（TACm）")
	flag.BoolVar(&cfg.keep, "keep", false, "結束後保留 datadir")
	flag.Parse()

	if cfg.bin == "" && cfg.url == "" {
		log.Fatal("必須指定 -bin <二進位> 或 -url <baseURL>")
	}
	if cfg.senders < 1 {
		cfg.senders = 1
	}
	if cfg.batchSize < 1 {
		cfg.batchSize = 1
	}

	var proc *nodeProc
	var err error
	if cfg.bin != "" {
		proc, err = startNode(cfg)
		if err != nil {
			log.Fatalf("啟動節點失敗: %v", err)
		}
		if !cfg.keep {
			defer func() { _ = os.RemoveAll(cfg.datadir) }()
		}
		defer proc.stop()
	}
	baseURL := cfg.url
	if proc != nil {
		baseURL = proc.baseURL
	}

	// funder：-bin 用 node_key；-url 用 -sender-key-hex。
	var funder *sender
	if cfg.senderKeyHex != "" {
		pk, err := decodeHex(cfg.senderKeyHex)
		if err != nil {
			log.Fatalf("解析 -sender-key-hex 失敗: %v", err)
		}
		kp, err := crypto.KeyPairFromPrivateKey(pk)
		if err != nil {
			log.Fatalf("載入私鑰失敗: %v", err)
		}
		funder = newSender(kp)
	} else if proc != nil {
		raw, err := os.ReadFile(filepath.Join(proc.dataDir, "node_key.json"))
		if err != nil {
			log.Fatalf("讀 node_key.json 失敗: %v", err)
		}
		var kd struct {
			PrivateKey string `json:"private_key"`
		}
		_ = json.Unmarshal(raw, &kd)
		pk, err := decodeHex(kd.PrivateKey)
		if err != nil {
			log.Fatalf("node_key 私鑰解碼失敗: %v", err)
		}
		kp, err := crypto.KeyPairFromPrivateKey(pk)
		if err != nil {
			log.Fatalf("載入節點金鑰失敗: %v", err)
		}
		funder = newSender(kp)
	} else {
		log.Fatal("-url 模式必須指定 -sender-key-hex")
	}

	// 產生 N 個負載 sender。
	senders := make([]*sender, cfg.senders)
	for i := range senders {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			log.Fatal(err)
		}
		senders[i] = newSender(kp)
	}
	recipKp, _ := crypto.GenerateKeyPair()
	recip, _ := recipKp.Address()

	fmt.Printf("loadtest: node=%s funder=%s senders=%d batch=%d mode=%s dur=%s\n",
		baseURL, funder.addr, cfg.senders, cfg.batchSize, cfg.mode, cfg.dur)

	dbBefore := int64(0)
	if proc != nil {
		dbBefore = dbSize(proc.dbPath)
	}

	// 1) 資金化。
	fmt.Println("loadtest: 資金化 senders ...")
	if err := fundSenders(baseURL, funder, senders, cfg.fundAmount); err != nil {
		log.Fatalf("資金化失敗: %v", err)
	}
	// 等資金交易真正入塊（chain accounts 已入帳），避免 load 第一波搶先送出。
	waitForSendersFunded(baseURL, 8, 45*time.Second)

	var (
		success int64
		failed  int64
		latMu   sync.Mutex
		latenc  []float64
	)
	stopCh := make(chan struct{})

	// 記憶體/DB/鏈高取樣。
	var memSamples []samplePoint
	sampleDone := make(chan struct{})
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-sampleDone:
				return
			case <-t.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				sp := samplePoint{
					T:            int64(time.Since(start).Seconds()),
					HeapAlloc:    ms.HeapAlloc,
					HeapInuse:    ms.HeapInuse,
					NumGC:        ms.NumGC,
					NumGoroutine: runtime.NumGoroutine(),
				}
				if proc != nil {
					sp.DBBytes = dbSize(proc.dbPath)
				}
				if h, err := chainHeight(baseURL); err == nil {
					sp.ChainHeight = h
				}
				latMu.Lock()
				memSamples = append(memSamples, sp)
				latMu.Unlock()
			}
		}
	}()

	amount := "0.000001"
	// 每 sender 的目標速率。
	perSenderRate := 0
	if cfg.rate > 0 {
		perSenderRate = cfg.rate / cfg.senders
		if perSenderRate < 1 {
			perSenderRate = 1
		}
	}

	var wg sync.WaitGroup
	for idx, sd := range senders {
		wg.Add(1)
		go func(s *sender, idx int) {
			defer wg.Done()
			// 啟動錯開：避免全部 sender 同一瞬間爆發第一波塞滿 mempool。
			select {
			case <-time.After(time.Duration(idx) * 80 * time.Millisecond):
			case <-stopCh:
				return
			}
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				var out batchOutcome
				if cfg.mode == "single" {
					out = sendSingle(baseURL, s, recip, amount)
				} else {
					out = sendBatchForSender(baseURL, s, recip, amount, cfg.batchSize)
				}
				atomic.AddInt64(&success, out.accepted)
				atomic.AddInt64(&failed, out.failed)
				if out.latencyMs > 0 {
					latMu.Lock()
					latenc = append(latenc, out.latencyMs)
					latMu.Unlock()
				}
				// 整批被拒（mempool 滿/節流）→ 退避，避免熱迴旋灌爆。
				if out.accepted == 0 && out.failed > 0 {
					select {
					case <-time.After(150 * time.Millisecond):
					case <-stopCh:
						return
					}
				}
				// 速率節流：按目標均勻投放。
				if perSenderRate > 0 {
					batchN := int64(cfg.batchSize)
					if cfg.mode == "single" {
						batchN = 1
					}
					perBatch := float64(batchN) / float64(perSenderRate)
					select {
					case <-time.After(time.Duration(perBatch * float64(time.Second))):
					case <-stopCh:
						return
					}
				}
			}
		}(sd, idx)
	}

	time.Sleep(cfg.dur)
	close(stopCh)
	wg.Wait()
	close(sampleDone)

	dbAfter := int64(0)
	if proc != nil {
		time.Sleep(2 * time.Second) // 等 mempool 打包
		dbAfter = dbSize(proc.dbPath)
	}
	hAfter, _ := chainHeight(baseURL)

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	succ := atomic.LoadInt64(&success)
	fail := atomic.LoadInt64(&failed)
	p50, p95, p99 := percentiles(latenc)
	total := succ + fail

	rpt := report{
		StartedAt:          time.Now().UTC().Format(time.RFC3339),
		DurationSec:        cfg.dur.Seconds(),
		Senders:            cfg.senders,
		BatchSize:          cfg.batchSize,
		Mode:               cfg.mode,
		RatePerSec:         cfg.rate,
		Success:            succ,
		Failed:             fail,
		ErrorRate:          safeDiv(fail, total),
		ActualTPS:          float64(succ) / cfg.dur.Seconds(),
		LatencyP50Ms:       p50,
		LatencyP95Ms:       p95,
		LatencyP99Ms:       p99,
		ChainDBBytesBefore: dbBefore,
		ChainDBBytesAfter:  dbAfter,
		ChainDBGrowthBytes: dbAfter - dbBefore,
		FinalHeapAlloc:     ms.HeapAlloc,
		FinalNumGC:         ms.NumGC,
		FinalNumGoroutine:  runtime.NumGoroutine(),
		MemSamples:         memSamples,
		Funder:             funder.addr,
		NodeURL:            baseURL,
		FinalChainHeight:   hAfter,
	}

	printReport(&rpt)
	if cfg.out != "" {
		f, _ := json.MarshalIndent(rpt, "", "  ")
		if err := os.WriteFile(cfg.out, f, 0o644); err != nil {
			log.Fatalf("寫報告失敗: %v", err)
		}
		fmt.Printf("報告已寫入 %s\n", cfg.out)
	}
}

// sendSingle 單筆模式：送出一筆，bad-nonce 校正後重送一次，回傳結果。
func sendSingle(baseURL string, s *sender, to, amount string) batchOutcome {
	for attempt := 0; attempt < 3; attempt++ {
		tx := map[string]any{
			"from": s.addr, "to": to, "amount": amount, "fee": "0",
			"nonce": s.nonce, "ts": time.Now().Unix(), "pubkey": s.pubHex,
		}
		sig, err := crypto.SignTransaction(tx, s.kp.PrivateKey())
		if err != nil {
			return batchOutcome{failed: 1}
		}
		tx["signature"] = sig
		start := time.Now()
		code, raw, err := postSingle(baseURL, tx)
		lat := time.Since(start).Seconds() * 1000
		if err != nil {
			time.Sleep(30 * time.Millisecond)
			continue
		}
		if code == 200 {
			s.nonce++
			return batchOutcome{accepted: 1, latencyMs: lat}
		}
		if exp, ok := parseExpectedNonce(raw); ok {
			s.nonce = exp
			continue
		}
		return batchOutcome{failed: 1, latencyMs: lat}
	}
	return batchOutcome{failed: 1}
}

func percentiles(lat []float64) (p50, p95, p99 float64) {
	if len(lat) == 0 {
		return 0, 0, 0
	}
	sorted := make([]float64, len(lat))
	copy(sorted, lat)
	sortFloats(sorted)
	at := func(q float64) float64 {
		idx := int(math.Ceil(q * float64(len(sorted))))
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		if idx < 0 {
			idx = 0
		}
		return sorted[idx]
	}
	return at(0.50), at(0.95), at(0.99)
}

func sortFloats(a []float64) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

func safeDiv(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func bytesHex(b []byte) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexChars[v>>4]
		out[i*2+1] = hexChars[v&0x0f]
	}
	return string(out)
}

func decodeHex(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 != 0 {
		return nil, errors.New("odd hex length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, err := strconv.ParseUint(s[i*2:i*2+1], 16, 8)
		if err != nil {
			return nil, err
		}
		lo, err := strconv.ParseUint(s[i*2+1:i*2+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out, nil
}

func printReport(r *report) {
	fmt.Println("\n===== TAC loadtest 報告 =====")
	fmt.Printf("節點 URL        : %s\n", r.NodeURL)
	fmt.Printf("模式 / senders  : %s / %d（batch=%d）\n", r.Mode, r.Senders, r.BatchSize)
	fmt.Printf("持續時間        : %.1fs\n", r.DurationSec)
	fmt.Printf("成功 / 失敗     : %d / %d\n", r.Success, r.Failed)
	fmt.Printf("錯誤率          : %.2f%%\n", r.ErrorRate*100)
	fmt.Printf("實際 TPS        : %.1f（入帳成功 tx/sec）\n", r.ActualTPS)
	fmt.Printf("延遲 p50/p95/p99: %.1f / %.1f / %.1f ms\n", r.LatencyP50Ms, r.LatencyP95Ms, r.LatencyP99Ms)
	fmt.Printf("最終鏈高        : %d\n", r.FinalChainHeight)
	fmt.Printf("chain.db 大小   : %d → %d bytes（增長 %+d）\n",
		r.ChainDBBytesBefore, r.ChainDBBytesAfter, r.ChainDBGrowthBytes)
	fmt.Printf("記憶體(本進程)  : HeapAlloc=%d KiB NumGC=%d Goroutine=%d\n",
		r.FinalHeapAlloc/1024, r.FinalNumGC, r.FinalNumGoroutine)
	if len(r.MemSamples) > 0 {
		fmt.Println("取樣趨勢(每秒 t, heap_inuse KiB, db bytes, chain_height):")
		for _, s := range r.MemSamples {
			fmt.Printf("  t=%2ds  heap_inuse=%8d KiB  db=%d  h=%d\n", s.T, s.HeapInuse/1024, s.DBBytes, s.ChainHeight)
		}
	}
	fmt.Println("=============================")
}
