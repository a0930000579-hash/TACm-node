// Package rollup 實現 TAC Ai 智能鏈的 Layer2 Optimistic Rollup：
// 交易在 L2 批量執行、狀態根提交到 Layer1、進入挑戰期（默認 7 天），
// 期間任何人可基於執行前快照真實重放提交欺詐證明；挑戰期過後最終確認。
// L1↔L2 經鎖定/鑄造方式出入金。L2 交易與 L1 同構（真實 ECDSA secp256k1、tx0 地址）。
package rollup

import "time"

// ZeroRoot 為空狀態根。
const ZeroRoot = "0x" + "0000000000000000000000000000000000000000000000000000000000000000"

// Config 為 Layer2 Rollup 配置。
type Config struct {
	DataDir           string
	MaxTxsPerBatch    int
	BatchInterval     time.Duration
	ChallengePeriod   int64 // 秒
	L1NodeURL         string
	L1ContractAddress string
	ProposerWIF       string // L1 提交者私鑰（WIF 或 64hex）
}

// DefaultConfig 默認配置（對齊 Python L2_CONFIG）。
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:           dataDir,
		MaxTxsPerBatch:    1000,
		BatchInterval:     5 * time.Second,
		ChallengePeriod:   604800, // 7 天
		L1NodeURL:         "http://127.0.0.1:8332",
		L1ContractAddress: "",
	}
}

// L2Account 為 L2 賬戶狀態。
type L2Account struct {
	Address     string  `json:"address"`
	Balance     float64 `json:"balance"`
	Nonce       int64   `json:"nonce"`
	StorageRoot string  `json:"storage_root"`
}

// L2Status 為交易狀態常量。
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusReverted  = "reverted"
)

// L2Transaction 為 L2 交易（與 L1 同構，真實 ECDSA 簽名）。
type L2Transaction struct {
	TxHash    string
	FromAddr  string
	ToAddr    string
	Amount    float64
	Fee       float64
	Nonce     int64
	Ts        int64
	Signature string
	Pubkey    string
	Status    string
	L2Block   int64 // 0=未打包
}

// L2 塊狀態常量。
const (
	BlockPending    = "pending"
	BlockSubmitted  = "submitted"
	BlockFinalized  = "finalized"
	BlockChallenged = "challenged"
	BlockReverted   = "reverted"
)

// L2Block 為 Rollup 批量塊。
type L2Block struct {
	Height        int64
	BatchIndex    int64
	TxCount       int
	TxHashes      []string
	StateRoot     string
	PrevStateRoot string
	Proposer      string
	Timestamp     int64
	L1TxHash      string
	Status        string
	PreSnapshot   *TreeSnapshot // 執行前完整快照（欺詐重放用）
}
