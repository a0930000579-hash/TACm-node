package vm

import "math/big"

// LogEntry 為合約事件日誌。
type LogEntry struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"` // 32 字節 hex；首項為事件簽名
	Data    string   `json:"data"`   // hex
}

// ExecutionContext 為單幀 EVM 執行上下文。
type ExecutionContext struct {
	Code     []byte
	Calldata []byte

	Address string // 當前執行賬戶（delegate 時為發起合約）
	Caller  string
	Origin  string

	CallValue *big.Int
	Gas       uint64
	GasPrice  *big.Int
	ChainID   *big.Int

	BlockNumber     int64
	BlockTimestamp  int64
	BlockCoinbase   string
	BlockDifficulty *big.Int
	BlockGasLimit   uint64
	BaseFee         *big.Int

	Logs       []LogEntry
	ReturnData []byte

	Reverted       bool
	RevertReason   string
	Stopped        bool
	SelfDestructed bool
}

// NewContext 返回帶默認值的上下文。
func NewContext() *ExecutionContext {
	return &ExecutionContext{
		Address: ZeroAddress, Caller: ZeroAddress, Origin: ZeroAddress,
		CallValue: new(big.Int), Gas: 10_000_000, GasPrice: new(big.Int),
		ChainID: big.NewInt(1337), BlockNumber: 1, BlockCoinbase: ZeroAddress,
		BlockDifficulty: new(big.Int), BlockGasLimit: 30_000_000,
		BaseFee: new(big.Int),
	}
}
