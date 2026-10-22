package vm

import (
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"strings"
)

// CallKind 為消息調用類型。
type CallKind int

const (
	CallKindCall CallKind = iota
	CallKindCallCode
	CallKindDelegate
	CallKindStatic
)

type callOpResult struct {
	ok                 bool
	outOffset, outSize uint64
}

func (in *Interpreter) messageCallOp(kind CallKind) *callOpResult {
	requestedGas, _ := in.pop()
	to, _ := in.pop()

	var value *big.Int
	if kind == CallKindCall || kind == CallKindCallCode {
		value, _ = in.pop()
	}
	inOff, _ := in.pop()
	inSize, _ := in.pop()
	outOff, _ := in.pop()
	outSize, _ := in.pop()

	var val *big.Int
	switch kind {
	case CallKindCall, CallKindCallCode:
		val = value
	case CallKindDelegate:
		val = nil
	case CallKindStatic:
		val = big.NewInt(0)
	}

	ok := in.doMessageCall(to, val, u64(inOff), u64(inSize),
		requestedGas.Uint64(), kind)

	return &callOpResult{ok: ok, outOffset: u64(outOff), outSize: u64(outSize)}
}

// inheritBlock 複製區塊環境到子上下文。
func inheritBlock(dst, src *ExecutionContext) {
	dst.GasPrice = src.GasPrice
	dst.ChainID = src.ChainID
	dst.BlockNumber = src.BlockNumber
	dst.BlockTimestamp = src.BlockTimestamp
	dst.BlockCoinbase = src.BlockCoinbase
	dst.BlockDifficulty = src.BlockDifficulty
	dst.BlockGasLimit = src.BlockGasLimit
	dst.BaseFee = src.BaseFee
}

// doMessageCall 執行消息調用；失敗（回滾/深度/餘額）返回 false。
// v136：執行級 call-frame 捕獲——以 named return + defer 統一填寫 trace。
func (in *Interpreter) doMessageCall(toBig, value *big.Int,
	inOff, inSize uint64, requestedGas uint64, kind CallKind) (ok bool) {

	to := NormalizeAddress(toBig)

	if err := in.memoryExtend(inOff, inSize); err != nil {
		return false
	}
	input := append([]byte(nil), in.memory[inOff:inOff+inSize]...)

	var execAddr, caller, callValue any
	switch kind {
	case CallKindCall, CallKindStatic:
		execAddr, caller, callValue = to, in.ctx.Address, value
	case CallKindCallCode:
		execAddr, caller, callValue = in.ctx.Address, in.ctx.Caller, value
	case CallKindDelegate:
		execAddr, caller, callValue = in.ctx.Address, in.ctx.Caller, in.ctx.CallValue
	}

	if in.depth >= MaxCallDepth {
		return false
	}

	// v136：trace frame 開始（type/from/to/value/gas/input）。
	callType := "call"
	switch kind {
	case CallKindCallCode:
		callType = "callcode"
	case CallKindDelegate:
		callType = "delegatecall"
	case CallKindStatic:
		callType = "staticcall"
	}
	from0x := in.ctx.Address // vm 內部地址一律 0x 格式（NormalizeAddress）
	to0x := NormalizeAddress(execAddr)
	prevCur := in.ctx.curTrace
	frameIdx := in.ctx.BeginTrace(callType, from0x, to0x,
		toBigInt(callValue), requestedGas, input)
	defer func() { in.ctx.EndTrace(frameIdx, prevCur, in.lastReturnData, in.gasUsed, !ok) }()

	snap := in.world.Snapshot()

	// 帶值轉賬。
	if (kind == CallKindCall || kind == CallKindCallCode) &&
		value != nil && value.Sign() > 0 {
		if kind == CallKindCall {
			if !in.world.Transfer(in.ctx.Address, to, value) {
				in.world.Revert(snap)
				return false
			}
		} else if in.world.GetBalance(in.ctx.Address).Cmp(value) < 0 {
			in.world.Revert(snap)
			return false
		}
	}

	// M134-A：EVM precompile（0x01-0x09）優先於合約碼執行。
	if out, matched, pcGas, pcErr := runPrecompile(in.ctx, toBig, input); matched {
		if pcErr != nil {
			in.world.Revert(snap)
			return false
		}
		_ = out
		if err := in.addChildGas(pcGas); err != nil {
			in.world.Revert(snap)
			return false
		}
		in.lastReturnData = out
		return true
	}

	code := in.world.GetCode(to)
	if len(code) == 0 {
		in.lastReturnData = nil
		return true // EOA：僅轉賬成功
	}

	stipend := in.childGas(requestedGas)

	childCtx := NewContext()
	inheritBlock(childCtx, in.ctx)
	childCtx.Code = code
	childCtx.Calldata = input
	childCtx.Address = NormalizeAddress(execAddr)
	childCtx.Caller = NormalizeAddress(caller)
	childCtx.Origin = in.ctx.Origin
	childCtx.CallValue = toBigInt(callValue)
	childCtx.Gas = stipend

	childCtx.Trace = in.ctx.Trace
		childCtx.CollectSteps = in.ctx.CollectSteps // 共享 trace 底層（子調用 frame 直接 append 進同一樹）
	childCtx.curTrace = frameIdx
	child := NewInterpreter(childCtx, in.world, in.depth+1,
		in.static || kind == CallKindStatic)
	result := child.Execute(nil, nil)
	in.ctx.Trace = childCtx.Trace // 取最新 header（append 可能 realloc）
	in.lastReturnData = result.ReturnData

	if err := in.addChildGas(result.GasUsed); err != nil {
		return false
	}
	if result.Reverted {
		in.world.Revert(snap)
		return false
	}
	in.ctx.Logs = append(in.ctx.Logs, result.Logs...)
	return true
}

func toBigInt(v any) *big.Int {
	if v == nil {
		return new(big.Int)
	}
	if b, ok := v.(*big.Int); ok {
		return b
	}
	return new(big.Int)
}

// childGas 計算子調用可用 gas（寬鬆 63/64 規則，對照 Python）。
func (in *Interpreter) childGas(requested uint64) uint64 {
	remaining := in.remainingGas()
	if requested < remaining {
		remaining = requested
	}
	available := remaining
	if full := in.remainingGas(); full > 200000 {
		available = full
	}
	stipend := available - available/64
	if requested < stipend {
		stipend = requested
	}
	return stipend
}

// writeCallOutput 把子調用返回數據寫入內存（可截斷/補零）。
func (in *Interpreter) writeCallOutput(offset, size uint64) {
	if size == 0 || len(in.lastReturnData) == 0 {
		return
	}
	if err := in.memoryExtend(offset, size); err != nil {
		return
	}
	n := int(size)
	if n > len(in.lastReturnData) {
		n = len(in.lastReturnData)
	}
	copy(in.memory[offset:offset+uint64(n)], in.lastReturnData[:n])
}

// createContract 執行 CREATE/CREATE2；返回合約地址（失敗為 0）。
// v136：trace frame（type=create）捕獲。
func (in *Interpreter) createContract(value *big.Int, offset, size uint64,
	salt *big.Int) (addr *big.Int, err error) {

	if err := in.memoryExtend(offset, size); err != nil {
		return nil, err
	}
	initCode := append([]byte(nil), in.memory[offset:offset+size]...)

	creator := in.ctx.Address
	creatorNonce := in.world.GetNonce(creator)

	var contractAddr string
	if salt == nil {
		contractAddr = CreateAddress(creator, creatorNonce)
	} else {
		contractAddr = Create2Address(creator, salt, initCode)
	}

	prevCur := in.ctx.curTrace
	frameIdx := in.ctx.BeginTrace("create", creator, contractAddr,
		toBigInt(value), uint64(in.remainingGas()), initCode)
	defer func() {
		reverted := addr == nil || addr.Sign() == 0
		in.ctx.EndTrace(frameIdx, prevCur, in.lastReturnData, in.gasUsed, reverted)
	}()

	snap := in.world.Snapshot()

	if value != nil && value.Sign() > 0 {
		if !in.world.Transfer(creator, contractAddr, value) {
			in.world.Revert(snap)
			return big.NewInt(0), nil
		}
	}

	in.world.IncNonce(creator)
	in.world.Ensure(contractAddr)

	// 創建 gas（寬鬆 63/64，無 requested cap）。
	full := in.remainingGas()
	stipend := full
	if full > 200000 {
		stipend = full - full/64
	}

	childCtx := NewContext()
	inheritBlock(childCtx, in.ctx)
	childCtx.Code = initCode
	childCtx.Address = contractAddr
	childCtx.Caller = creator
	childCtx.Origin = in.ctx.Origin
	childCtx.CallValue = toBigInt(value)
	childCtx.Gas = stipend
	childCtx.Trace = in.ctx.Trace
	childCtx.curTrace = frameIdx

	child := NewInterpreter(childCtx, in.world, in.depth+1, false)
	result := child.Execute(nil, nil)
	in.ctx.Trace = childCtx.Trace

	if err := in.addChildGas(result.GasUsed); err != nil {
		return big.NewInt(0), nil
	}
	if result.Reverted {
		in.world.Revert(snap)
		return big.NewInt(0), nil
	}

	runtimeCode := result.ReturnData
	if len(runtimeCode) > MaxCodeSize {
		in.world.Revert(snap)
		return big.NewInt(0), nil
	}
	in.world.SetCode(contractAddr, runtimeCode)
	in.ctx.Logs = append(in.ctx.Logs, result.Logs...)
	return addrToInt(contractAddr), nil
}

// CreateAddress 為標準 CREATE 合約地址：keccak256(creator20 ++ nonce32) 末20。
func CreateAddress(creator string, nonce uint64) string {
	cb, _ := hex.DecodeString(strings.TrimPrefix(creator, "0x"))
	nb := make([]byte, 32)
	binary.BigEndian.PutUint64(nb[24:], nonce)
	return NormalizeAddress(Keccak256(cb, nb)[12:])
}

// Create2Address 為 CREATE2 地址：
// keccak256(0xff ++ creator ++ salt32 ++ keccak256(initcode)) 末20。
func Create2Address(creator string, salt *big.Int, initCode []byte) string {
	cb, _ := hex.DecodeString(strings.TrimPrefix(creator, "0x"))
	addrB := Keccak256([]byte{0xff}, cb, IntToBytes(salt, 32), Keccak256(initCode))
	return NormalizeAddress(addrB[12:])
}
