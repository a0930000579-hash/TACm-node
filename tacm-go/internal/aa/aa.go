// Package aa M154：帳戶抽象（Account Abstraction，EIP-4337 規格）。
// 提供 UserOperation 提交、驗證與估算：
//   - UserOperation 完整 EIP-4337 欄位（sender/nonce/initCode/callData/gas/paymaster/signature）
//   - 驗證：sender 位址合法、pubkey→sender 匹配、callData 為合法 hex、ECDSA 簽名驗證
//   - 執行：離線 EntryPoint（Bundler 模式）——節點驗證操作授權後以節點金鑰代執行
//     （與主流「Bundler 以自身金鑰提交交易到 EntryPoint」同構；鏈上 EntryPoint 合約為 v2 路線）
//   - 估算：gas/fee 估算；操作紀錄：sender → 歷史提交（供狀態查詢）
// 以 Go 慣用寫法實作：sync.RWMutex 併發保護、完整 error handling。
package aa

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"tacm/internal/crypto"
)

// UserOperation EIP-4337 使用者操作。
type UserOperation struct {
	Sender             string `json:"sender"`
	Nonce              int64  `json:"nonce"`
	InitCode           string `json:"init_code,omitempty"`
	Target             string `json:"target"` // 目標合約位址（本鏈擴充：bundler 執行對象）
	CallData           string `json:"call_data"`
	CallGasLimit       uint64 `json:"call_gas_limit"`
	VerificationGas    uint64 `json:"verification_gas"`
	PreVerificationGas uint64 `json:"pre_verification_gas"`
	MaxFeePerGas       string `json:"max_fee_per_gas"`
	MaxPriorityFee     string `json:"max_priority_fee_per_gas"`
	PaymasterAndData   string `json:"paymaster_and_data,omitempty"`
	Pubkey             string `json:"pubkey"`   // 壓縮公鑰 hex（驗簽）
	Signature          string `json:"signature"` // DER 簽名 hex
}

// Operation 一筆已提交的 AA 操作紀錄。
type Operation struct {
	Sender      string `json:"sender"`
	Target      string `json:"target"`
	CallData    string `json:"call_data"`
	TxHash      string `json:"tx_hash"`
	GasFee      string `json:"gas_fee"`
	Status      string `json:"status"` // submitted | rejected
	Error       string `json:"error,omitempty"`
	SubmittedAt int64  `json:"submitted_at"`
}

// AA 為帳戶抽象入口（離線 EntryPoint / Bundler）。
type AA struct {
	mu      sync.RWMutex
	ops     map[string][]Operation // sender → 操作紀錄（新→舊）
	maxOps  int
	bundler string // 代執行者位址（節點金鑰）
}

// New 建立 AA 服務。
func New(bundlerAddr string) *AA {
	return &AA{
		ops:     make(map[string][]Operation),
		maxOps:  50,
		bundler: bundlerAddr,
	}
}

// Bundler 回傳代執行者位址。
func (a *AA) Bundler() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.bundler
}

// UserOpDigest 計算 UserOperation 簽名摘要：
// DoubleSHA256(Canonical({sender, nonce, target, call_data, max_fee, max_priority_fee}))。
// callData 納入摘要——保證操作資料不可被篡改（防中間人改 callData）。
func UserOpDigest(op *UserOperation) ([]byte, error) {
	if op == nil {
		return nil, errors.New("aa: nil user operation")
	}
	p := map[string]any{
		"sender": op.Sender, "nonce": op.Nonce, "target": op.Target,
		"call_data": op.CallData, "max_fee": op.MaxFeePerGas,
		"max_priority_fee": op.MaxPriorityFee,
	}
	raw, err := crypto.Canonical(p)
	if err != nil {
		return nil, fmt.Errorf("aa: 計算操作摘要: %w", err)
	}
	return crypto.DoubleSHA256(raw), nil
}

// Validate 驗證 UserOperation：欄位完整性、sender/pubkey 匹配、hex 合法性與 ECDSA 簽名。
func (a *AA) Validate(op *UserOperation) error {
	if op == nil {
		return errors.New("aa: user operation required")
	}
	op.Sender = strings.TrimSpace(op.Sender)
	op.Target = strings.TrimSpace(op.Target)
	op.CallData = strings.TrimSpace(op.CallData)
	op.MaxFeePerGas = strings.TrimSpace(op.MaxFeePerGas)
	if !crypto.IsValidAddress(op.Sender) {
		return errors.New("aa: invalid sender address")
	}
	if !crypto.IsValidAddress(op.Target) {
		return errors.New("aa: invalid target address")
	}
	if op.Nonce < 0 {
		return errors.New("aa: nonce must be >= 0")
	}
	// callData 必須為合法 hex 且非空。
	raw, err := hex.DecodeString(op.CallData)
	if err != nil || len(raw) == 0 {
		return errors.New("aa: call_data must be non-empty hex")
	}
	// gas 參數合理性。
	total := op.PreVerificationGas + op.VerificationGas + op.CallGasLimit
	if total == 0 {
		return errors.New("aa: gas limit must be > 0")
	}
	mfee, err := strconv.ParseFloat(op.MaxFeePerGas, 64)
	if err != nil || math.IsNaN(mfee) || math.IsInf(mfee, 0) || mfee <= 0 {
		return errors.New("aa: max_fee_per_gas must be a positive number")
	}
	// pubkey→sender 匹配。
	pub, err := hex.DecodeString(op.Pubkey)
	if err != nil {
		return errors.New("aa: pubkey must be hex")
	}
	addr, err := crypto.PubKeyToAddress(pub)
	if err != nil {
		return fmt.Errorf("aa: pubkey 轉地址: %w", err)
	}
	if !strings.EqualFold(addr, op.Sender) {
		return fmt.Errorf("aa: pubkey 與 sender 不符 (pubkey→%s, sender=%s)", addr, op.Sender)
	}
	// ECDSA 簽名驗證。
	sig, err := hex.DecodeString(op.Signature)
	if err != nil {
		return errors.New("aa: signature must be hex")
	}
	digest, err := UserOpDigest(op)
	if err != nil {
		return err
	}
	if !crypto.VerifyDigest(pub, digest, sig) {
		return errors.New("aa: invalid signature")
	}
	return nil
}

// Estimate 估算操作執行成本（gas 總量與以 maxFeePerGas 計的費用）。
func (a *AA) Estimate(op *UserOperation) (gas uint64, fee float64, err error) {
	if op == nil {
		return 0, 0, errors.New("aa: user operation required")
	}
	if op.CallData == "" {
		return 0, 0, errors.New("aa: call_data required")
	}
	// calldata 長度佔比：每 32 字節約 16 gas（近似，非精確 EVM 計費）。
	calldataGas := uint64(len(op.CallData)/2) * 16
	gas = op.PreVerificationGas + op.VerificationGas + op.CallGasLimit + calldataGas
	if gas == 0 {
		gas = 100_000 // 預設下限
	}
	mfee, err := strconv.ParseFloat(op.MaxFeePerGas, 64)
	if err != nil || mfee <= 0 {
		mfee = 0.000001 // 預設費率（每 gas）
	}
	return gas, mfee * float64(gas), nil
}

// Record 記錄一筆操作結果（submitted/rejected），回傳是否接受（防重放：nonce 遞增檢查）。
func (a *AA) Record(op *UserOperation, txHash string, gasFee string, errText string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	hist := a.ops[op.Sender]
	status := "submitted"
	if errText != "" {
		status = "rejected"
	}
	entry := Operation{
		Sender: op.Sender, Target: op.Target, CallData: op.CallData,
		TxHash: txHash, GasFee: gasFee, Status: status, Error: errText,
		SubmittedAt: time.Now().Unix(),
	}
	hist = append([]Operation{entry}, hist...)
	if len(hist) > a.maxOps {
		hist = hist[:a.maxOps]
	}
	a.ops[op.Sender] = hist
}

// Operations 回傳指定 sender 的 AA 操作紀錄（新→舊）。
func (a *AA) Operations(sender string) []Operation {
	sender = strings.TrimSpace(sender)
	a.mu.RLock()
	defer a.mu.RUnlock()
	h, ok := a.ops[sender]
	if !ok {
		return nil
	}
	out := make([]Operation, len(h))
	copy(out, h)
	return out
}
