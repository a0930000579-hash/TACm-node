package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"tacm/internal/crypto"
)

// AssetConfig 為一個跨鏈資產的 BSC 側映射設定（M149r：資產互通深化——多資產互通）。
type AssetConfig struct {
	Token            string  `json:"token"`             // 鏈上資產鍵（tacm / tiusd / usdt）
	BSCTokenAddr     string  `json:"bsc_token"`         // BSC 側映射代幣（0x）
	BSCLockProxyAddr string  `json:"bsc_lock_proxy"`    // BSC 側 LockProxy（0x）
	MinAmount        float64 `json:"min_amount"`        // 最小中繼金額（過濾 dust）
}

// AssetsFromEnv 讀環境變數 TAC_BSC_ASSETS（JSON 陣列）建多資產清單。
// 格式：[{"token":"tiusd","bsc_token":"0x…","bsc_lock_proxy":"0x…","min_amount":1}]
// env 未設或解析失敗回 nil；非法項（token 空／地址非 0x）自動過濾——
// 部署合約後無需改程式碼，僅需設定環境變數並重啟即可互通 TiUSD/USDT。
func AssetsFromEnv() []AssetConfig {
	return AssetsFromEnvNamed("TAC_BSC_ASSETS")
}

// AssetsFromEnvNamed 讀指定環境變數建多資產清單（M150h：多鏈各自 TAC_<CHAIN>_ASSETS）。
func AssetsFromEnvNamed(envName string) []AssetConfig {
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return nil
	}
	var out []AssetConfig
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	valid := out[:0]
	for _, a := range out {
		tok := strings.TrimSpace(a.Token)
		bt := strings.ToLower(strings.TrimSpace(a.BSCTokenAddr))
		lp := strings.ToLower(strings.TrimSpace(a.BSCLockProxyAddr))
		if tok != "" && strings.HasPrefix(bt, "0x") && strings.HasPrefix(lp, "0x") {
			valid = append(valid, a)
		}
	}
	return valid
}

// RelayerConfig 為跨鏈雙向中繼器設定（M150h：ChainName 支援多鏈實例）。
type RelayerConfig struct {
	ChainName        string        // 鏈名（bsc/ethereum/polygon…）；空則預設 "bsc"
	BSCTestRPC       string        // 鏈 RPC URL——發送/執行視角
	BSCTestScanRPC   string        // 掃描專用 RPC（eth_getLogs/eth_blockNumber）；空則與 BSCTestRPC 共用
	BSCPrivateKeyHex string        // relayer 鏈側簽署私鑰（hex）
	BSCChainID       int64         // 鏈 ID（97=測試網 / 56=主網；ethereum=11155111 等）
	BSCTokenAddr     string        // 鏈側 TACM 映射代幣（0x，舊欄位；Assets 未含 tacm 時直填）
	BSCLockProxyAddr string        // 鏈側 LockProxy（0x，舊欄位）
	PollInterval     time.Duration // 輪詢間隔
	Confirmations    int64         // 掃描確認數（建議 ≥12）
	MinAmount        float64       // 最小中繼金額（TACm，過濾 dust）
	AssetsEnv        string        // 資產環境變數名（預設 "TAC_BSC_ASSETS"；eth 用 "TAC_ETH_ASSETS"）
	// Assets 多資產互通清單：空時以環境變數/舊欄位建單一 TACM 資產（向後相容）。
	Assets []AssetConfig
}

// DefaultRelayerConfig 回默認中繼設定（測試網友善）。
func DefaultRelayerConfig() RelayerConfig {
	return RelayerConfig{
		ChainName:     "bsc",
		BSCChainID:    BSCTestnetChainID,
		PollInterval:  10 * time.Second,
		Confirmations: 0,
		MinAmount:     0.001,
	}
}

// Relayer 為 TAC ↔ 目標鏈雙向自動中繼（M150h：支援多鏈實例）：
//   - TAC→鏈：掃描橋上已鎖定（locked）的 tacm→鏈 交易，在目標鏈 mint 映射代幣。
//   - 鏈→TAC：掃描目標鏈 LockProxy 的 Deposit 事件，在 TAC 側建立解鎖記錄
//     （burning）交由守衛/節點既有流程完成鏈上解鎖。
type Relayer struct {
	cfg       RelayerConfig
	bridge    *Bridge
	chainName string
	client    *BSCClient
	contract  *BridgeContract            // 默認 TACM 合約（回退用）
	contracts map[string]*BridgeContract // M149r：每資產（token→鏈合約）
	signer    *EthSigner

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRelayer 建中繼器並校驗設定。
func NewRelayer(cfg RelayerConfig, b *Bridge) (*Relayer, error) {
	if cfg.ChainName == "" {
		cfg.ChainName = "bsc"
	}
	if cfg.AssetsEnv == "" {
		cfg.AssetsEnv = "TAC_BSC_ASSETS"
	}
	cfg.BSCTestRPC = strings.TrimSpace(cfg.BSCTestRPC)
	if cfg.BSCTestRPC == "" {
		return nil, fmt.Errorf("relayer(%s): RPC URL 為空", cfg.ChainName)
	}
	if b == nil {
		return nil, errors.New("relayer: bridge 為空")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 10 * time.Second
	}
	if cfg.BSCChainID <= 0 {
		cfg.BSCChainID = BSCTestnetChainID
	}
	if cfg.MinAmount <= 0 {
		cfg.MinAmount = 0.001
	}
	client, err := NewBSCClient(cfg.BSCTestRPC, 20*time.Second)
	if err != nil {
		return nil, err
	}
	client.SetScanURL(cfg.BSCTestScanRPC)
	// M149s：多資產互通——Assets 未指定時，先嘗試資產環境變數（M150h：按鏈名分離）；
	// env 也未提供時以舊欄位建單一 TACM 資產（向後相容）。
	if len(cfg.Assets) == 0 {
		if envAssets := AssetsFromEnvNamed(cfg.AssetsEnv); len(envAssets) > 0 {
			cfg.Assets = envAssets
		} else {
			cfg.Assets = []AssetConfig{{
				Token: "tacm", BSCTokenAddr: cfg.BSCTokenAddr,
				BSCLockProxyAddr: cfg.BSCLockProxyAddr, MinAmount: cfg.MinAmount,
			}}
		}
	}
	contracts := make(map[string]*BridgeContract, len(cfg.Assets))
	for _, a := range cfg.Assets {
		if strings.TrimSpace(a.Token) == "" {
			return nil, fmt.Errorf("relayer(%s): 資產 Token 為空", cfg.ChainName)
		}
		addr, proxy := a.BSCTokenAddr, a.BSCLockProxyAddr
		if addr == "" {
			addr = cfg.BSCTokenAddr
		}
		if proxy == "" {
			proxy = cfg.BSCLockProxyAddr
		}
		if addr == "" || proxy == "" {
			return nil, fmt.Errorf("relayer: 資產 %s 的 BSC 代幣/LockProxy 地址不完整", a.Token)
		}
		c, err := NewBridgeContract(client, addr, proxy)
		if err != nil {
			return nil, err
		}
		contracts[strings.ToLower(a.Token)] = c
	}
	var signer *EthSigner
	if strings.TrimSpace(cfg.BSCPrivateKeyHex) != "" {
		signer, err = NewEthSigner(cfg.BSCPrivateKeyHex)
		if err != nil {
			return nil, err
		}
	}
	return &Relayer{
		cfg:       cfg,
		bridge:    b,
		chainName: cfg.ChainName,
		client:    client,
		contracts: contracts,
		signer:    signer,
	}, nil
}

// contractFor 依資產鍵選 BSC 合約（M149r）：缺省回退默認 TACM 合約。
func (r *Relayer) contractFor(token string) *BridgeContract {
	if c, ok := r.contracts[strings.ToLower(token)]; ok {
		return c
	}
	if c, ok := r.contracts["tacm"]; ok {
		return c
	}
	return r.contract
}

// Run 啟動雙向中繼 goroutine；ctx 取消即停止。
func (r *Relayer) Run(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(2)
	go r.loop(ctx, "tacm→"+r.chainName, r.relayTACtoBSC)
	go r.loop(ctx, r.chainName+"→tacm", r.relayBSCtoTAC)
	log.Printf("["+r.chainName+"-relay] 啟動：rpc=%s chain=%d token=%s lockproxy=%s 簽署=%v",
		r.cfg.BSCTestRPC, r.cfg.BSCChainID, r.cfg.BSCTokenAddr, r.cfg.BSCLockProxyAddr, r.signer != nil)
}

func (r *Relayer) loop(ctx context.Context, name string, fn func(context.Context) error) {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	// 啟動即跑一輪。
	if err := fn(ctx); err != nil {
		log.Printf("["+r.chainName+"-relay] %s 初始輪失敗: %v", name, err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := fn(ctx); err != nil {
				log.Printf("["+r.chainName+"-relay] %s 輪詢失敗: %v", name, err)
			}
		}
	}
}

// Config 回中繼設定。
func (r *Relayer) Config() RelayerConfig { return r.cfg }

// Signer 回 BSC 側簽署器（未配置回 nil）。
func (r *Relayer) Signer() *EthSigner { return r.signer }

// LastScanned 回最近已掃描的 BSC 區塊高度。
func (r *Relayer) LastScanned() (int64, error) { return r.lastScannedBlock() }

// Close 停止中繼並等待 goroutine 結束。
func (r *Relayer) Close() error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

// relayTACtoBSC 單輪：掃描 locked 的 tacm→bsc 交易並在 BSC mint。
func (r *Relayer) relayTACtoBSC(ctx context.Context) error {
	if r.signer == nil {
		return errors.New("relayer: 未配置 BSC 私鑰，無法執行 mint")
	}
	pending, err := r.bridge.store.GetPendingBridgeTxs()
	if err != nil {
		return err
	}
	for i := range pending {
		tx := &pending[i]
		if !strings.EqualFold(tx.SourceChain, "tacm") ||
			!strings.EqualFold(tx.TargetChain, r.chainName) ||
			tx.Status != StatusLocked {
			continue
		}
		// 金額換算：BridgeTx.Amount 為 TACm → BSC mint 需 wei（×1e18）。
		amountWei := tacmToWei(tx.ReceivedAmount)
		if amountWei.Sign() <= 0 {
			continue
		}
		// 目標地址：BridgeTx.TargetAddress 為 TAC tx0 → 乙太坊風 0x 需轉換。
		to0x, err := tacAddrToEth0x(tx.TargetAddress)
		if err != nil {
			r.markError(tx, fmt.Errorf("目標地址轉換失敗: %w", err))
			continue
		}
		from := r.signer.Address()
		contract := r.contractFor(tx.Token)
		// M149u：mint 冪等——SendMint 上鏈成功後立即把 BSC tx hash 寫入 config
		// （bsc_mint_<BridgeTxID>）；若本地 MintOnTarget 記錄失敗，下一輪重試
		// 只補記錄、不重發 mint，避免 BSC 側重複 mint（超發）。
		mintKey := r.chainName + "_mint_" + tx.BridgeTxID
		if saved, err := r.bridge.store.GetConfig(mintKey); err == nil && saved != "" {
			if _, err := r.bridge.MintOnTarget(tx.BridgeTxID, saved); err != nil {
				r.markError(tx, fmt.Errorf("mint 補記錄失敗: %w", err))
				continue
			}
			log.Printf("["+r.chainName+"-relay] TAC→%s mint 補記錄（先前已上鏈） id=%s tx=%s", r.chainName, tx.BridgeTxID, saved)
			continue
		}
		// M150b：中繼 mint 改走 TACBridge.withdraw（onlyOwner，橋合約內部 mint 映射代幣）；
		// 直接 mint 映射代幣會被 onlyBridge 拒絕。
		// M150f：symbol 須與合約 deployMapped 註冊名一致（小寫 tacm/tiusd/usdt）；
		// BridgeTx.Token 為大寫（TACM），直接傳入會觸發合約 revert "unknown symbol"。
		contractSymbol := strings.ToLower(tx.Token)
		h, err := contract.SendWithdraw(ctx, from, r.signer,
			contractSymbol, to0x, amountWei, big.NewInt(tx.Nonce), r.cfg.BSCChainID)
		if err != nil {
			r.markError(tx, err)
			continue
		}
		if werr := r.bridge.store.SetConfig(mintKey, h); werr != nil {
			log.Printf("["+r.chainName+"-relay] 寫 mint hash 失敗 %s: %v", mintKey, werr)
		}
		if _, err := r.bridge.MintOnTarget(tx.BridgeTxID, h); err != nil {
			r.markError(tx, fmt.Errorf("mint 記錄失敗: %w", err))
			continue
		}
		log.Printf("["+r.chainName+"-relay] TAC→%s mint 完成 id=%s amount=%s wei tx=%s", r.chainName, tx.BridgeTxID, amountWei.String(), h)
	}
	return nil
}

// relayBSCtoTAC 單輪：掃描 BSC Deposit 事件並在 TAC 側建立解鎖記錄。
func (r *Relayer) relayBSCtoTAC(ctx context.Context) error {
	last, err := r.lastScannedBlock()
	if err != nil {
		return err
	}
	var latest int64
	if err := withRetry(ctx, 3, func() error {
		v, err := r.client.BlockNumber(ctx)
		if err != nil {
			return err
		}
		latest = v
		return nil
	}); err != nil {
		return fmt.Errorf("取 BSC 最新區塊失敗（重試後）: %w", err)
	}
	toBlock := latest - r.cfg.Confirmations
	if toBlock < 0 {
		toBlock = 0
	}
	// M150a：首次掃描——合約剛部署無歷史事件，若歷史區間過長（>10 萬塊）
	// 直接從當前已確認高度起掃，避免觸發 BSC 公共 RPC eth_getLogs limit exceeded；
	// 短歷史（測試/舊合約）仍從 0 起掃，確保不漏事件。
	if last <= 0 && toBlock > 100000 {
		last = toBlock
	}
	fromBlock := last + 1
	if fromBlock > toBlock {
		return nil // 無新區塊
	}
	// M150a：分段掃描——公共 RPC 對 eth_getLogs 有單次範圍/數量上限，
	// 且 publicnode 僅保留最近 ~3000 塊的 logs 索引；每段 ≤30 塊逐段處理，
	// 段間 sleep 防限流（publicnode 對密集請求會靜默返回空）。
	// 該段任一資產查詢失敗則停在段首，下輪重掃該段（高度不推進，避免跳過 Deposit 事件）。
	const scanSegment = 30
	for seg := fromBlock; seg <= toBlock; seg += scanSegment {
		segTo := seg + scanSegment - 1
		if segTo > toBlock {
			segTo = toBlock
		}
		// M149r：多資產互通——每資產掃描其 LockProxy 的 Deposit（同一區間），
		// 任一資產掃描失敗不阻斷其他資產。
		segFailed := false
		for _, a := range r.cfg.Assets {
			if err := r.scanAssetDeposits(ctx, a, seg, segTo); err != nil {
				log.Printf("["+r.chainName+"-relay] 資產 %s Deposit 掃描失敗: %v", a.Token, err)
				segFailed = true
			}
		}
		if segFailed {
			break // 停在失敗段，下輪從 seg 重掃
		}
		if err := r.setScannedBlock(segTo); err != nil {
			return err
		}
		// M150b：公共 RPC 限流緩衝——非最後一段之間保持節奏，
		// 避免 publicnode 因密集 eth_getLogs 靜默返回空。
		if seg+scanSegment <= toBlock {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	return nil
}

// scanAssetDeposits 掃描單一資產的 BSC Deposit 事件並建立解鎖記錄。
func (r *Relayer) scanAssetDeposits(ctx context.Context, a AssetConfig, fromBlock, toBlock int64) error {
	depositTopic := "0x" + crypto.HexEncode(crypto.Keccak256([]byte(EventDeposit)))
	var logs []BSClog
	if err := withRetry(ctx, 3, func() error {
		v, err := r.client.Logs(ctx, fromBlock, toBlock,
			[]string{a.BSCLockProxyAddr},
			[][]string{{depositTopic}})
		if err != nil {
			return err
		}
		logs = v
		return nil
	}); err != nil {
		return fmt.Errorf("掃描 %s Deposit 日誌失敗（重試後）: %w", a.Token, err)
	}
	events, err := ParseDepositLogs(logs, a.BSCTokenAddr)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if err := r.handleDeposit(ctx, ev, a); err != nil {
			log.Printf("["+r.chainName+"-relay] 處理 %s Deposit 失敗 %s（將於下一輪重試）: %v", a.Token, ev.TxHash, err)
		}
	}
	return nil
}

// handleDeposit 對單筆 BSC Deposit 建立 TAC 側解鎖記錄並確認（M149r：多資產）。
func (r *Relayer) handleDeposit(ctx context.Context, ev DepositEvent, a AssetConfig) error {
	// M149t：Deposit 事件冪等去重——以 (txHash, logIndex) 為鍵，防止「處理成功
	// 但確認失敗／水位未推進」時下一輪重掃重複建立解鎖單（雙花風險）。
	evtKey := r.chainEventKey(ev.TxHash, ev.LogIndex)
	if v, err := r.bridge.store.GetConfig(evtKey); err == nil && v != "" {
		log.Printf("["+r.chainName+"-relay] 已處理事件跳過 %s（%s）", ev.TxHash, a.Token)
		return nil
	}
	if ev.Amount == nil || ev.Amount.Sign() <= 0 {
		return errors.New("金額非正")
	}
	minAmt := a.MinAmount
	if minAmt <= 0 {
		minAmt = r.cfg.MinAmount
	}
	amountTacm := weiToTacmFloat(ev.Amount)
	if amountTacm < minAmt {
		// 低於最小中繼的 dust 事件：寫鍵永久跳過，避免每輪重試同一筆。
		if werr := r.bridge.store.SetConfig(evtKey, "1"); werr != nil {
			log.Printf("["+r.chainName+"-relay] 寫去重鍵失敗 %s: %v", evtKey, werr)
		}
		return nil
	}
	// 收款方：Deposit 事件含乙太坊風 to（20B）；TAC 側解鎖目標地址
	// 由節點/守衛在執行鏈上解鎖時按既有映射解析。此處以事件 to 的 tx0 映射建單。
	target, err := eth0xToTacAddr(ev.To)
	if err != nil {
		return err
	}
	token := a.Token
	if token == "" {
		token = "tacm"
	}
	// M149s 修復：targetChain 固定為 tacm（TAC 側解鎖目標鏈），token 獨立傳遞——
	// 誤將 token 當 targetChain 會導致「unsupported target chain」。
	res, err := r.bridge.BurnAndUnlock(r.chainName, "tacm", ev.From, target, amountTacm, strings.ToUpper(token))
	if err != nil {
		return err
	}
	// M149t：建單成功即寫去重鍵——即使後續 ConfirmBurn 失敗，下輪重掃
	// 也不會重複建單（單已存在，鍵防護雙花）。
	if err := r.bridge.store.SetConfig(evtKey, "1"); err != nil {
		log.Printf("["+r.chainName+"-relay] 寫去重鍵失敗 %s: %v", evtKey, err)
	}
	if _, err := r.bridge.ConfirmBurn(res.BridgeTxID, ev.TxHash); err != nil {
		return fmt.Errorf("確認 BSC 銷毀失敗: %w", err)
	}
	// M150d：自動完成目標鏈解鎖（burning→confirmed）。目前 TAC 側尚無獨立
	// 跨鏈解鎖交易，以 BSC deposit 證明 hash 作為解鎖憑證；待 TAC 側跨鏈
	// 交易實作後改填真實 TAC tx hash。unlock 寫庫失敗僅警告、不阻斷輪詢。
	if _, err := r.bridge.UnlockOnTarget(res.BridgeTxID, ev.TxHash); err != nil {
		log.Printf("["+r.chainName+"-relay] 自動確認目標鏈解鎖失敗（單停留 burning）id=%s: %v", res.BridgeTxID, err)
	}
	log.Printf("["+r.chainName+"-relay] %s→TAC 解鎖單已建立 id=%s amount=%.8f %s tx=%s", r.chainName,
		res.BridgeTxID, amountTacm, strings.ToUpper(token), ev.TxHash)
	return nil
}

// markError 在橋交易上記錄錯誤（不阻塞其他交易）。
func (r *Relayer) markError(tx *BridgeTx, err error) {
	log.Printf("["+r.chainName+"-relay] 交易 %s 標記錯誤: %v", tx.BridgeTxID, err)
	tx.Error = err.Error()
	tx.UpdatedAt = time.Now().Unix()
	if uerr := r.bridge.store.UpdateBridgeTx(tx); uerr != nil {
		log.Printf("["+r.chainName+"-relay] 記錄錯誤失敗 %s: %v", tx.BridgeTxID, uerr)
	}
}

// chainEventKey 組目標鏈 Deposit 事件去重鍵（存於橋 store config 表）：
// (txHash, logIndex) 唯一標識一筆事件；水位重掃或失敗重試時依鍵跳過。
// M150h：鍵前綴按鏈名分離，避免多鏈事件互相碰撞。
func (r *Relayer) chainEventKey(txHash string, logIdx int) string {
	return fmt.Sprintf(r.chainName+"_evt_%s_%d", strings.ToLower(strings.TrimPrefix(strings.TrimSpace(txHash), "0x")), logIdx)
}

// withRetry 對 BSC RPC 讀取類呼叫做有限次指數退避重試（公共 RPC 網路瞬斷自癒）。
// 僅用於無副作用查詢（區塊高度／日誌）；絕不用於 sendRawTransaction——
// 重試會造成重複提交，該風險由 M149u mint 冪等與 nonce 序列保護。
func withRetry(ctx context.Context, attempts int, fn func() error) error {
	var err error
	delay := 500 * time.Millisecond
	for i := 1; i <= attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i < attempts {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
			delay *= 2
		}
	}
	return err
}

// 掃描進度（存於橋 store 的 config 表）。

func (r *Relayer) lastScannedBlock() (int64, error) {
	v, err := r.bridge.store.GetConfig(r.chainName + "_last_scanned")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(v, 10); !ok {
		return 0, fmt.Errorf("relayer: 掃描高度非法 %q", v)
	}
	if !n.IsInt64() {
		return 0, errors.New("relayer: 掃描高度超出 int64")
	}
	return n.Int64(), nil
}

func (r *Relayer) setScannedBlock(h int64) error {
	if h < 0 {
		h = 0
	}
	return r.bridge.store.SetConfig(r.chainName+"_last_scanned", fmt.Sprintf("%d", h))
}

// 單位換算工具。

// tacmToWei 把 TACm 十進位金額（float64）轉 wei（×1e18）。
func tacmToWei(tacm float64) *big.Int {
	f := new(big.Float).Mul(big.NewFloat(tacm), big.NewFloat(1e18))
	i, _ := f.Int(nil)
	return i
}

// weiToTacmFloat 把 wei 轉 TACm float64（÷1e18）。
func weiToTacmFloat(wei *big.Int) float64 {
	f, _ := new(big.Float).SetInt(wei).Float64()
	return f / 1e18
}

// tacAddrToEth0x 把目標地址標準化為 0x 乙太坊格式（冪等）：
// 已是合法 0x 地址（BSC 側直填）原樣回傳；TAC tx0 則轉 Hash160。
func tacAddrToEth0x(tx0 string) (string, error) {
	if _, err := crypto.EthAddressBytes(tx0); err == nil {
		return normalize0x(tx0), nil
	}
	b, err := crypto.AddressToHash160(tx0)
	if err != nil {
		return "", fmt.Errorf("TAC 地址轉換失敗: %w", err)
	}
	return crypto.EthAddressHex(b), nil
}

// eth0xToTacAddr 把乙太坊風 0x 地址轉 TAC tx0（Hash160ToAddress）。
func eth0xToTacAddr(eth0x string) (string, error) {
	b, err := crypto.EthAddressBytes(eth0x)
	if err != nil {
		return "", err
	}
	return crypto.Hash160ToAddress(b), nil
}
