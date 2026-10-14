package bridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"tacm/internal/crypto"
)

// RelayerConfig 為 BSC 雙向中繼器設定。
type RelayerConfig struct {
	BSCTestRPC       string        // BSC 測試網 RPC URL（主網亦可）
	BSCPrivateKeyHex string        // relayer BSC 側簽署私鑰（hex）
	BSCChainID       int64         // 97=測試網 / 56=主網
	BSCTokenAddr     string        // BSC 側 TACM 映射代幣（0x）
	BSCLockProxyAddr string        // BSC 側 LockProxy（0x）
	PollInterval     time.Duration // 輪詢間隔
	Confirmations    int64         // BSC 掃描確認數（建議 ≥12）
	MinAmount        float64       // 最小中繼金額（TACm，過濾 dust）
}

// DefaultRelayerConfig 回默認中繼設定（測試網友善）。
func DefaultRelayerConfig() RelayerConfig {
	return RelayerConfig{
		BSCChainID:    BSCTestnetChainID,
		PollInterval:  10 * time.Second,
		Confirmations: 0,
		MinAmount:     0.001,
	}
}

// Relayer 為 TAC ↔ BSC 雙向自動中繼：
//   - TAC→BSC：掃描橋上已鎖定（locked）的 tacm→bsc 交易，在 BSC 側 mint 映射代幣。
//   - BSC→TAC：掃描 BSC LockProxy 的 Deposit 事件，在 TAC 側建立解鎖記錄
//     （burning）交由守衛/節點既有流程完成鏈上解鎖。
type Relayer struct {
	cfg      RelayerConfig
	bridge   *Bridge
	client   *BSCClient
	contract *BridgeContract
	signer   *EthSigner

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRelayer 建中繼器並校驗設定。
func NewRelayer(cfg RelayerConfig, b *Bridge) (*Relayer, error) {
	cfg.BSCTestRPC = strings.TrimSpace(cfg.BSCTestRPC)
	if cfg.BSCTestRPC == "" {
		return nil, errors.New("relayer: BSC RPC URL 為空")
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
	contract, err := NewBridgeContract(client, cfg.BSCTokenAddr, cfg.BSCLockProxyAddr)
	if err != nil {
		return nil, err
	}
	var signer *EthSigner
	if strings.TrimSpace(cfg.BSCPrivateKeyHex) != "" {
		signer, err = NewEthSigner(cfg.BSCPrivateKeyHex)
		if err != nil {
			return nil, err
		}
	}
	return &Relayer{
		cfg:      cfg,
		bridge:   b,
		client:   client,
		contract: contract,
		signer:   signer,
	}, nil
}

// Run 啟動雙向中繼 goroutine；ctx 取消即停止。
func (r *Relayer) Run(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.wg.Add(2)
	go r.loop(ctx, "tacm→bsc", r.relayTACtoBSC)
	go r.loop(ctx, "bsc→tacm", r.relayBSCtoTAC)
	log.Printf("[bsc-relay] 啟動：rpc=%s chain=%d token=%s lockproxy=%s 簽署=%v",
		r.cfg.BSCTestRPC, r.cfg.BSCChainID, r.cfg.BSCTokenAddr, r.cfg.BSCLockProxyAddr, r.signer != nil)
}

func (r *Relayer) loop(ctx context.Context, name string, fn func(context.Context) error) {
	defer r.wg.Done()
	t := time.NewTicker(r.cfg.PollInterval)
	defer t.Stop()
	// 啟動即跑一輪。
	if err := fn(ctx); err != nil {
		log.Printf("[bsc-relay] %s 初始輪失敗: %v", name, err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := fn(ctx); err != nil {
				log.Printf("[bsc-relay] %s 輪詢失敗: %v", name, err)
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
			!strings.EqualFold(tx.TargetChain, "bsc") ||
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
		h, err := r.contract.SendMint(ctx, from, r.signer, to0x, amountWei, r.cfg.BSCChainID)
		if err != nil {
			r.markError(tx, err)
			continue
		}
		if _, err := r.bridge.MintOnTarget(tx.BridgeTxID, h); err != nil {
			r.markError(tx, fmt.Errorf("mint 記錄失敗: %w", err))
			continue
		}
		log.Printf("[bsc-relay] TAC→BSC mint 完成 id=%s amount=%s wei bsc_tx=%s", tx.BridgeTxID, amountWei.String(), h)
	}
	return nil
}

// relayBSCtoTAC 單輪：掃描 BSC Deposit 事件並在 TAC 側建立解鎖記錄。
func (r *Relayer) relayBSCtoTAC(ctx context.Context) error {
	last, err := r.lastScannedBlock()
	if err != nil {
		return err
	}
	latest, err := r.client.BlockNumber(ctx)
	if err != nil {
		return err
	}
	toBlock := latest - r.cfg.Confirmations
	if toBlock < 0 {
		toBlock = 0
	}
	fromBlock := last + 1
	if fromBlock > toBlock {
		return nil // 無新區塊
	}
	depositTopic := "0x" + crypto.HexEncode(crypto.Keccak256([]byte(EventDeposit)))
	logs, err := r.client.Logs(ctx, fromBlock, toBlock,
		[]string{r.cfg.BSCLockProxyAddr},
		[][]string{{depositTopic}})
	if err != nil {
		return err
	}
	events, err := ParseDepositLogs(logs, r.cfg.BSCTokenAddr)
	if err != nil {
		return err
	}
	for _, ev := range events {
		if err := r.handleDeposit(ctx, ev); err != nil {
			log.Printf("[bsc-relay] 處理 Deposit 失敗 %s（將於下一輪重試）: %v", ev.TxHash, err)
		}
	}
	if err := r.setScannedBlock(toBlock); err != nil {
		return err
	}
	return nil
}

// handleDeposit 對單筆 BSC Deposit 建立 TAC 側解鎖記錄並確認。
func (r *Relayer) handleDeposit(ctx context.Context, ev DepositEvent) error {
	if ev.Amount == nil || ev.Amount.Sign() <= 0 {
		return errors.New("金額非正")
	}
	amountTacm := weiToTacmFloat(ev.Amount)
	if amountTacm < r.cfg.MinAmount {
		return fmt.Errorf("金額 %.8f 低於最小中繼 %.8f", amountTacm, r.cfg.MinAmount)
	}
	// 收款方：Deposit 事件含乙太坊風 to（20B）；TAC 側解鎖目標地址
	// 由節點/守衛在執行鏈上解鎖時按既有映射解析。此處以事件 to 的 tx0 映射建單。
	target, err := eth0xToTacAddr(ev.To)
	if err != nil {
		return err
	}
	res, err := r.bridge.BurnAndUnlock("bsc", "tacm", ev.From, target, amountTacm, "TACM")
	if err != nil {
		return err
	}
	if _, err := r.bridge.ConfirmBurn(res.BridgeTxID, ev.TxHash); err != nil {
		return fmt.Errorf("確認 BSC 銷毀失敗: %w", err)
	}
	log.Printf("[bsc-relay] BSC→TAC 解鎖單已建立 id=%s amount=%.8f TACM bsc_tx=%s",
		res.BridgeTxID, amountTacm, ev.TxHash)
	return nil
}

// markError 在橋交易上記錄錯誤（不阻塞其他交易）。
func (r *Relayer) markError(tx *BridgeTx, err error) {
	tx.Error = err.Error()
	tx.UpdatedAt = time.Now().Unix()
	if uerr := r.bridge.store.UpdateBridgeTx(tx); uerr != nil {
		log.Printf("[bsc-relay] 記錄錯誤失敗 %s: %v", tx.BridgeTxID, uerr)
	}
}

// 掃描進度（存於橋 store 的 config 表）。

func (r *Relayer) lastScannedBlock() (int64, error) {
	v, err := r.bridge.store.GetConfig("bsc_last_scanned")
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
	return r.bridge.store.SetConfig("bsc_last_scanned", fmt.Sprintf("%d", h))
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

// tacAddrToEth0x 把 TAC tx0 地址轉乙太坊風 0x 地址（Hash160 20B）。
func tacAddrToEth0x(tx0 string) (string, error) {
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
