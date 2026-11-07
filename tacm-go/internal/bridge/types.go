// Package bridge 實現 TAC Ai 智能鏈的跨鏈橋：Lock & Mint / Burn & Unlock，
// 跨鏈消息以真實 ECDSA（secp256k1）多重簽名守衛，支持多條公鏈。
// 不引入任何外部鏈客戶端；外部鏈對接僅以消息/證明形式由守衛網絡驗證。
package bridge

// ChainInfo 描述一條可對接的鏈。
type ChainInfo struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Coin    string `json:"coin"`
	ChainID int64  `json:"chain_id"`
}

// SupportedChains 返回橋當前支持的鏈。
func SupportedChains() map[string]ChainInfo {
	return map[string]ChainInfo{
		"tacm":     {Key: "tacm", Name: "TACm Chain", Coin: "TACM", ChainID: 8888},
		"ethereum": {Key: "ethereum", Name: "Ethereum", Coin: "ETH", ChainID: 1},
		"bsc":      {Key: "bsc", Name: "BNB Smart Chain", Coin: "BNB", ChainID: 56},
		"polygon":  {Key: "polygon", Name: "Polygon", Coin: "MATIC", ChainID: 137},
		"arbitrum": {Key: "arbitrum", Name: "Arbitrum", Coin: "ETH", ChainID: 42161},
		"optimism": {Key: "optimism", Name: "Optimism", Coin: "ETH", ChainID: 10},
	}
}

// 跨鏈交易狀態。
const (
	StatusPending   = "pending"
	StatusLocked    = "locked"
	StatusMinted    = "minted"
	StatusBurning   = "burning"
	StatusUnlocked  = "unlocked"
	StatusConfirmed = "confirmed"
	StatusFailed    = "failed"
	StatusRefunded  = "refunded"
)

// ValidatorSig 為驗證人對跨鏈消息/交易的簽名項。
type ValidatorSig struct {
	Validator string `json:"validator"`
	Signature string `json:"signature"`
	Ts        int64  `json:"timestamp"`
}

// BridgeTx 為一筆跨鏈轉移記錄。
type BridgeTx struct {
	ID             int64          `json:"id"`
	BridgeTxID     string         `json:"bridge_tx_id"`
	SourceChain    string         `json:"source_chain"`
	TargetChain    string         `json:"target_chain"`
	SourceAddress  string         `json:"source_address"`
	TargetAddress  string         `json:"target_address"`
	Amount         float64        `json:"amount"`
	Fee            float64        `json:"fee"`
	ReceivedAmount float64        `json:"received_amount"`
	Token          string         `json:"token"`
	Status         string         `json:"status"`
	SourceTxHash   string         `json:"source_tx_hash,omitempty"`
	TargetTxHash   string         `json:"target_tx_hash,omitempty"`
	LockTxHash     string         `json:"lock_tx_hash,omitempty"`
	UnlockTxHash   string         `json:"unlock_tx_hash,omitempty"`
	Nonce          int64          `json:"nonce"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
	ConfirmedAt    int64          `json:"confirmed_at,omitempty"`
	Error          string         `json:"error,omitempty"`
	Signatures     []ValidatorSig `json:"signatures"`
}

// Validator 為跨鏈守衛驗證人。
type Validator struct {
	Address   string `json:"address"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Active    bool   `json:"is_active"`
	JoinedAt  int64  `json:"joined_at"`
}

// Config 為跨鏈橋參數。
type Config struct {
	DataDir            string
	RequiredSignatures int
	FeeRate            float64
	MinAmount          float64
	LockTime           int64
}

// DefaultConfig 返回默認跨鏈橋配置。
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:            dataDir,
		RequiredSignatures: 2,
		FeeRate:            0.001,
		MinAmount:          0.001,
		LockTime:           300,
	}
}
