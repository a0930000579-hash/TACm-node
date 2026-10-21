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

	// v134：EIP-1153 瞬態儲存（tx 級，CALL 間共享；新交易前需清空）。
	transient map[string]map[string]*big.Int

	// v136：執行級 call-frame 捕獲（trace 樹；新執行上下文預設 nil、零開銷）。
	Trace    []*TraceFrame
	curTrace int // 目前執行中的 frame idx（-1=根層）

	// v137：逐 opcode 步驟收集（geth debug structLogs 對應；僅 CollectSteps=true 收集）。
	CollectSteps bool
	Steps        []*ExecStep
}

// ExecStep 執行器單步記錄（structLogs 元素對應）。
type ExecStep struct {
	PC      uint64
	Op      string   // "0x01"
	OpName  string   // "ADD"
	Gas     uint64   // 執行該步後剩餘 gas
	GasCost uint64
	Depth   int
	Stack   []string // 棧頂至多 4 項（hex）
}

// TraceFrame 單一 call frame（Parity/OpenEthereum trace 樹節點）。
type TraceFrame struct {
	Type     string // call / callcode / delegatecall / staticcall / create
	From     string // 0x 格式
	To       string // 0x 格式（create 為新合約地址）
	Value    *big.Int
	Gas      uint64
	Input    []byte
	Output   []byte
	GasUsed  uint64 // v138：frame 實際消耗 gas（EndTrace 填）
	Reverted bool
	Parent   int // parent frame idx，-1=根
	Sibling  int // 同 parent 下的序號（traceAddress 用）
	Depth    int
}

// BeginTrace 記錄 call frame 開始，回傳 frame idx；呼叫方需以 EndTrace 收尾。
func (c *ExecutionContext) BeginTrace(typ, from, to string,
	value *big.Int, gas uint64, input []byte) int {
	if c.Trace == nil {
		c.Trace = []*TraceFrame{}
		c.curTrace = -1
	}
	sibling := 0
	for _, f := range c.Trace {
		if f.Parent == c.curTrace {
			sibling++
		}
	}
	idx := len(c.Trace)
	c.Trace = append(c.Trace, &TraceFrame{
		Type: typ, From: from, To: to,
		Value:  new(big.Int).Set(value),
		Gas:    gas, Input: append([]byte(nil), input...),
		Parent: c.curTrace, Sibling: sibling, Depth: c.curTrace + 1,
	})
	c.curTrace = idx
	return idx
}

// EndTrace 填寫 frame 結果並還原執行指標（prev 為 BeginTrace 前的 curTrace）。
func (c *ExecutionContext) EndTrace(idx, prev int, out []byte, gasUsed uint64, reverted bool) {
	if idx >= 0 && idx < len(c.Trace) {
		c.Trace[idx].Output = append([]byte(nil), out...)
		c.Trace[idx].GasUsed = gasUsed
		c.Trace[idx].Reverted = reverted
	}
	c.curTrace = prev
}

// TransientSlot 讀瞬態槽（無→0）。
func (c *ExecutionContext) TransientSlot(addr string, key *big.Int) *big.Int {
	if c.transient == nil {
		return big.NewInt(0)
	}
	ks := key.String()
	if m, ok := c.transient[addr]; ok {
		if v, ok2 := m[ks]; ok2 {
			return v
		}
	}
	return big.NewInt(0)
}

// TransientStore 寫瞬態槽（0 值刪除）。
func (c *ExecutionContext) TransientStore(addr string, key, value *big.Int) {
	if c.transient == nil {
		c.transient = make(map[string]map[string]*big.Int)
	}
	ks := key.String()
	if value.Sign() == 0 {
		if m, ok := c.transient[addr]; ok {
			delete(m, ks)
		}
		return
	}
	m, ok := c.transient[addr]
	if !ok {
		m = make(map[string]*big.Int)
		c.transient[addr] = m
	}
	m[ks] = new(big.Int).Set(value)
}

// ResetTransient 清空瞬態儲存（交易開始時呼叫）。
func (c *ExecutionContext) ResetTransient() { c.transient = nil }

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
