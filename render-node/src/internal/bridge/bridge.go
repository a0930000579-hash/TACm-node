package bridge

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"tacm/internal/crypto"
)

// ErrBridgePaused 為管理員緊急暫停（kill-switch）時拒絕橋操作的錯誤。
var ErrBridgePaused = errors.New("bridge is paused by admin (kill-switch)")

// Bridge 為跨鏈橋核心狀態機。
type Bridge struct {
	cfg   Config
	store *Store
	mu    sync.Mutex
	nonce int64
	// M143：緊急暫停開關（kill-switch）——paused=1 時所有橋操作被拒絕，防駭客/異常時資金損失。
	paused int32

	// 守衛網絡（M11）：本節點簽名身份、全網守衛列表與多簽驗證器。
	guardianKP   *crypto.KeyPair
	guardianAddr string
	validators   []Validator
	verifier     *MessageVerifier
	// proposals 緩存收到的跨鏈提案（messageID → 消息），供簽名聚合與執行。
	proposals map[string]*CrossChainMessage
}

// New 建立跨鏈橋並初始化持久配置。
func New(cfg Config) (*Bridge, error) {
	if cfg.RequiredSignatures <= 0 {
		cfg.RequiredSignatures = 2
	}
	if cfg.FeeRate <= 0 {
		cfg.FeeRate = 0.001
	}
	if cfg.MinAmount <= 0 {
		cfg.MinAmount = 0.001
	}
	store, err := OpenStore(filepath.Join(cfg.DataDir, "bridge.db"))
	if err != nil {
		return nil, err
	}
	b := &Bridge{cfg: cfg, store: store, proposals: map[string]*CrossChainMessage{}}
	if err := b.initConfig(); err != nil {
		_ = store.Close()
		return nil, err
	}
	// M143：載入持久化的暫停狀態（kill-switch 重啟後保持）。
	if v, err := store.GetConfig("paused"); err == nil && v == "1" {
		atomic.StoreInt32(&b.paused, 1)
	}
	return b, nil
}

// SetPaused 設定緊急暫停狀態並持久化；true=暫停（拒絕所有橋操作），false=恢復。
func (b *Bridge) SetPaused(v bool) error {
	vstr := "0"
	if v {
		vstr = "1"
	}
	if err := b.store.SetConfig("paused", vstr); err != nil {
		return fmt.Errorf("bridge: 持久化暫停狀態失敗: %w", err)
	}
	if v {
		atomic.StoreInt32(&b.paused, 1)
	} else {
		atomic.StoreInt32(&b.paused, 0)
	}
	return nil
}

// IsPaused 回報緊急暫停狀態。
func (b *Bridge) IsPaused() bool { return atomic.LoadInt32(&b.paused) == 1 }

// checkPaused 在橋操作入口檢查 kill-switch；已暫停時回傳 ErrBridgePaused。
func (b *Bridge) checkPaused() error {
	if b.IsPaused() {
		return ErrBridgePaused
	}
	return nil
}

func (b *Bridge) initConfig() error {
	defaults := [][2]string{
		{"fee_rate", strconv.FormatFloat(b.cfg.FeeRate, 'f', -1, 64)},
		{"min_amount", strconv.FormatFloat(b.cfg.MinAmount, 'f', -1, 64)},
		{"lock_time", strconv.FormatInt(b.cfg.LockTime, 10)},
		{"required_signatures", strconv.Itoa(b.cfg.RequiredSignatures)},
	}
	for _, kv := range defaults {
		if existing, err := b.store.GetConfig(kv[0]); err != nil {
			return err
		} else if existing == "" {
			if err := b.store.SetConfig(kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close 關閉持久層。
func (b *Bridge) Close() error { return b.store.Close() }

func (b *Bridge) feeRate() (float64, error) {
	raw, err := b.store.GetConfig("fee_rate")
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return b.cfg.FeeRate, nil
	}
	return strconv.ParseFloat(raw, 64)
}

func (b *Bridge) minAmount() (float64, error) {
	raw, err := b.store.GetConfig("min_amount")
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return b.cfg.MinAmount, nil
	}
	return strconv.ParseFloat(raw, 64)
}

// ActionResult 為跨鏈操作返回。
type ActionResult struct {
	OK          bool    `json:"ok"`
	BridgeTxID  string  `json:"bridge_tx_id"`
	Status      string  `json:"status"`
	SourceChain string  `json:"source_chain"`
	TargetChain string  `json:"target_chain"`
	Amount      float64 `json:"amount"`
	Fee         float64 `json:"fee"`
	Received    float64 `json:"received_amount"`
	Message     string  `json:"message"`
}

func (b *Bridge) validateChains(source, target string) error {
	chains := SupportedChains()
	if _, ok := chains[source]; !ok {
		return fmt.Errorf("unsupported source chain: %s", source)
	}
	if _, ok := chains[target]; !ok {
		return fmt.Errorf("unsupported target chain: %s", target)
	}
	if source == target {
		return fmt.Errorf("source and target chain must differ")
	}
	return nil
}

func (b *Bridge) createTransfer(source, target, srcAddr, tgtAddr string,
	amount float64, token string) (*ActionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.checkPaused(); err != nil {
		return nil, err
	}
	if err := b.validateChains(source, target); err != nil {
		return nil, err
	}
	minAmt, err := b.minAmount()
	if err != nil {
		return nil, err
	}
	if amount < minAmt {
		return nil, fmt.Errorf("amount below minimum %g", minAmt)
	}
	rate, err := b.feeRate()
	if err != nil {
		return nil, err
	}
	fee := round8(amount * rate)
	received := round8(amount - fee)
	if token == "" {
		token = "TACM"
	}

	b.nonce++
	now := time.Now().Unix()
	seed := fmt.Sprintf("%s:%s:%s:%d:%d", source, target, srcAddr, now, b.nonce)
	bridgeTxID := "bridge-" + hex.EncodeToString(crypto.SHA256([]byte(seed)))[:32]

	tx := &BridgeTx{
		BridgeTxID:  bridgeTxID,
		SourceChain: source, TargetChain: target,
		SourceAddress: srcAddr, TargetAddress: tgtAddr,
		Amount: amount, Fee: fee, ReceivedAmount: received,
		Token: token, Status: StatusPending, Nonce: b.nonce,
		CreatedAt: now, UpdatedAt: now,
		Signatures: []ValidatorSig{},
	}
	if err := b.store.CreateBridgeTx(tx); err != nil {
		return nil, err
	}
	return &ActionResult{
		OK: true, BridgeTxID: bridgeTxID, Status: StatusPending,
		SourceChain: source, TargetChain: target,
		Amount: amount, Fee: fee, Received: received,
		Message: "submitted; awaiting source confirmation",
	}, nil
}

// LockAndMint 正向跨鏈：源鏈鎖定 → 目標鏈鑄造。
func (b *Bridge) LockAndMint(sourceChain, targetChain,
	sourceAddress, targetAddress string, amount float64, token string) (*ActionResult, error) {
	res, err := b.createTransfer(sourceChain, targetChain,
		sourceAddress, targetAddress, amount, token)
	if err != nil {
		return nil, err
	}
	res.Message = "assets submitted; will lock on source then mint on target"
	return res, nil
}

// BurnAndUnlock 反向跨鏈：源鏈銷毀 → 目標鏈解鎖。
func (b *Bridge) BurnAndUnlock(sourceChain, targetChain,
	sourceAddress, targetAddress string, amount float64, token string) (*ActionResult, error) {
	res, err := b.createTransfer(sourceChain, targetChain,
		sourceAddress, targetAddress, amount, token)
	if err != nil {
		return nil, err
	}
	res.Message = "assets submitted; will burn on source then unlock on target"
	return res, nil
}

func (b *Bridge) loadAndCheck(id, wantStatus string) (*BridgeTx, error) {
	tx, err := b.store.GetBridgeTx(id)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("bridge transaction not found")
	}
	if tx.Status != wantStatus {
		return nil, fmt.Errorf("invalid status %s, expected %s",
			tx.Status, wantStatus)
	}
	return tx, nil
}

// ConfirmLock 確認源鏈鎖定：pending → locked。
func (b *Bridge) ConfirmLock(bridgeTxID, sourceTxHash string) (*BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkPaused(); err != nil {
		return nil, err
	}
	tx, err := b.loadAndCheck(bridgeTxID, StatusPending)
	if err != nil {
		return nil, err
	}
	tx.Status = StatusLocked
	tx.SourceTxHash = sourceTxHash
	tx.LockTxHash = sourceTxHash
	tx.UpdatedAt = time.Now().Unix()
	if err := b.store.UpdateBridgeTx(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// MintOnTarget 在目標鏈鑄造：locked → minted → confirmed。
func (b *Bridge) MintOnTarget(bridgeTxID, targetTxHash string) (*BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkPaused(); err != nil {
		return nil, err
	}
	tx, err := b.loadAndCheck(bridgeTxID, StatusLocked)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	tx.Status = StatusConfirmed
	tx.TargetTxHash = targetTxHash
	tx.UpdatedAt = now
	tx.ConfirmedAt = now
	if err := b.store.UpdateBridgeTx(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// ConfirmBurn 確認源鏈銷毀：pending → burning。
func (b *Bridge) ConfirmBurn(bridgeTxID, sourceTxHash string) (*BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkPaused(); err != nil {
		return nil, err
	}
	tx, err := b.loadAndCheck(bridgeTxID, StatusPending)
	if err != nil {
		return nil, err
	}
	tx.Status = StatusBurning
	tx.SourceTxHash = sourceTxHash
	tx.UpdatedAt = time.Now().Unix()
	if err := b.store.UpdateBridgeTx(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// UnlockOnTarget 在目標鏈解鎖：burning → unlocked → confirmed。
func (b *Bridge) UnlockOnTarget(bridgeTxID, unlockTxHash string) (*BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkPaused(); err != nil {
		return nil, err
	}
	tx, err := b.loadAndCheck(bridgeTxID, StatusBurning)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	tx.Status = StatusConfirmed
	tx.UnlockTxHash = unlockTxHash
	tx.TargetTxHash = unlockTxHash
	tx.UpdatedAt = now
	tx.ConfirmedAt = now
	if err := b.store.UpdateBridgeTx(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// Status 查詢單筆跨鏈交易。
func (b *Bridge) Status(bridgeTxID string) (*BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loadAndCheckAny(bridgeTxID)
}

func (b *Bridge) loadAndCheckAny(id string) (*BridgeTx, error) {
	tx, err := b.store.GetBridgeTx(id)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("bridge transaction not found")
	}
	return tx, nil
}

// ByAddress 查詢地址相關跨鏈交易。
func (b *Bridge) ByAddress(address string, limit int) ([]BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	return b.store.GetBridgeTxsByAddress(address, limit)
}

// Stats 跨鏈橋統計。
func (b *Bridge) Stats() (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending, err := b.store.GetPendingBridgeTxs()
	if err != nil {
		return nil, err
	}
	rate, _ := b.feeRate()
	return map[string]any{
		"pending_count":    len(pending),
		"supported_chains": len(SupportedChains()),
		"fee_rate":         rate,
		"paused":           b.IsPaused(),
	}, nil
}

// AddValidator 註冊守衛驗證人。
func (b *Bridge) AddValidator(v Validator) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.AddValidator(v)
}

// ActiveValidators 返回活躍守衛驗證人。
func (b *Bridge) ActiveValidators() ([]Validator, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.store.GetActiveValidators()
}

func round8(v float64) float64 { return math.Round(v*1e8) / 1e8 }
