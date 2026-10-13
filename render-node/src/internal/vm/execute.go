package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
)

// ExecutionResult 為一次字節碼執行的結果。
type ExecutionResult struct {
	Success        bool       `json:"success"`
	GasUsed        uint64     `json:"gas_used"`
	ReturnData     []byte     `json:"return_data"`
	Stack          []*big.Int `json:"-"`
	Logs           []LogEntry `json:"logs"`
	Reverted       bool       `json:"reverted"`
	RevertReason   string     `json:"revert_reason,omitempty"`
	SelfDestructed bool       `json:"self_destructed"`
	Err            string     `json:"error,omitempty"`
}

func addrToInt(addr string) *big.Int {
	b, _ := hex.DecodeString(strings.TrimPrefix(addr, "0x"))
	return BytesToInt(b)
}

func u64(x *big.Int) uint64 { return x.Uint64() }

func failResult(in *Interpreter, err error) *ExecutionResult {
	return &ExecutionResult{
		Success: false, GasUsed: in.gasUsed,
		Stack: in.stack, Logs: in.ctx.Logs, Reverted: true,
		RevertReason: err.Error(), Err: err.Error(),
		SelfDestructed: in.ctx.SelfDestructed,
	}
}

// Execute 執行字節碼；code/calldata 非空時覆蓋上下文。
func (in *Interpreter) Execute(code, calldata []byte) *ExecutionResult {
	if code != nil {
		in.ctx.Code = code
	}
	if calldata != nil {
		in.ctx.Calldata = calldata
	}

	// 當前賬戶必須存在。
	in.ctx.Address = in.world.Ensure(in.ctx.Address)

	in.pc = 0
	in.stack = in.stack[:0]
	in.memory = nil
	in.gasUsed = 0
	in.lastReturnData = nil
	in.ctx.Reverted = false
	in.ctx.Stopped = false
	in.ctx.ReturnData = nil

	codeBytes := in.ctx.Code
	calldata = in.ctx.Calldata
	steps := 0

	for in.pc < len(codeBytes) {
		steps++
		if steps > MaxExecSteps {
			return failResult(in, ErrStepLimit)
		}

		op := codeBytes[in.pc]
		name, known := OpcodeNames[op]
		if !known {
			name = "UNKNOWN"
		}
		if err := in.consumeGas(name); err != nil {
			return failResult(in, err)
		}

		advance := true

		switch op {
		// ---- 0x0 算術 ----
		case 0x00: // STOP
			in.ctx.Stopped = true
			return in.finish(false)

		case 0x01: // ADD
			a, _ := in.pop()
			b, _ := in.pop()
			if err := in.push(new(big.Int).Add(a, b)); err != nil {
				return failResult(in, err)
			}
		case 0x02: // MUL
			a, _ := in.pop()
			b, _ := in.pop()
			if err := in.push(new(big.Int).Mul(a, b)); err != nil {
				return failResult(in, err)
			}
		case 0x03: // SUB: 次 - 頂
			a, _ := in.pop()
			b, _ := in.pop()
			if err := in.push(U256(new(big.Int).Sub(b, a))); err != nil {
				return failResult(in, err)
			}
		case 0x04: // DIV: 次 // 頂
			a, _ := in.pop()
			b, _ := in.pop()
			r := new(big.Int)
			if a.Sign() != 0 {
				r.Div(b, a)
			}
			_ = in.push(r)
		case 0x05: // SDIV
			a, _ := in.pop()
			b, _ := in.pop()
			sa, sb := ToSigned(a), ToSigned(b)
			r := new(big.Int)
			if sa.Sign() != 0 {
				r.Quo(sb, sa)
			}
			_ = in.push(FromSigned(r))
		case 0x06: // MOD: 次 % 頂
			a, _ := in.pop()
			b, _ := in.pop()
			r := new(big.Int)
			if a.Sign() != 0 {
				r.Mod(b, a)
			}
			_ = in.push(r)
		case 0x07: // SMOD
			a, _ := in.pop()
			b, _ := in.pop()
			sa, sb := ToSigned(a), ToSigned(b)
			r := new(big.Int)
			if sa.Sign() != 0 {
				m := new(big.Int).Mod(sb, new(big.Int).Abs(sa))
				if sb.Sign() < 0 {
					m.Neg(m)
				}
				r = m
			}
			_ = in.push(FromSigned(r))
		case 0x08: // ADDMOD
			a, _ := in.pop()
			b, _ := in.pop()
			n, _ := in.pop()
			r := new(big.Int)
			if n.Sign() != 0 {
				r.Mod(new(big.Int).Add(a, b), n)
			}
			_ = in.push(r)
		case 0x09: // MULMOD
			a, _ := in.pop()
			b, _ := in.pop()
			n, _ := in.pop()
			r := new(big.Int)
			if n.Sign() != 0 {
				r.Mod(new(big.Int).Mul(a, b), n)
			}
			_ = in.push(r)
		case 0x0A: // EXP: 棧頂 exponent，次 base
			exponent, _ := in.pop()
			base, _ := in.pop()
			r := new(big.Int).Exp(base, exponent, tt256)
			_ = in.push(r)
		case 0x0B: // SIGNEXTEND: 頂 b，次 x
			b, _ := in.pop()
			x, _ := in.pop()
			bi := b.Uint64()
			if bi < 31 {
				signBit := new(big.Int).Lsh(big.NewInt(1), uint(bi*8+7))
				if new(big.Int).And(x, signBit).Sign() != 0 {
					mask := new(big.Int).Sub(signBit, big.NewInt(1))
					x.Or(x, new(big.Int).Xor(MaxUint256, mask))
				} else {
					x.And(x, new(big.Int).Sub(signBit, big.NewInt(1)))
				}
			}
			_ = in.push(x)

		// ---- 0x1 比較/位 ----
		case 0x10: // LT
			b, _ := in.pop()
			a, _ := in.pop()
			_ = in.push(boolBig(a.Cmp(b) < 0))
		case 0x11: // GT
			b, _ := in.pop()
			a, _ := in.pop()
			_ = in.push(boolBig(a.Cmp(b) > 0))
		case 0x12: // SLT
			b, _ := in.pop()
			a, _ := in.pop()
			_ = in.push(boolBig(ToSigned(a).Cmp(ToSigned(b)) < 0))
		case 0x13: // SGT
			b, _ := in.pop()
			a, _ := in.pop()
			_ = in.push(boolBig(ToSigned(a).Cmp(ToSigned(b)) > 0))
		case 0x14: // EQ
			a, _ := in.pop()
			b, _ := in.pop()
			_ = in.push(boolBig(a.Cmp(b) == 0))
		case 0x15: // ISZERO
			a, _ := in.pop()
			_ = in.push(boolBig(a.Sign() == 0))
		case 0x16: // AND
			a, _ := in.pop()
			b, _ := in.pop()
			_ = in.push(new(big.Int).And(a, b))
		case 0x17: // OR
			a, _ := in.pop()
			b, _ := in.pop()
			_ = in.push(new(big.Int).Or(a, b))
		case 0x18: // XOR
			a, _ := in.pop()
			b, _ := in.pop()
			_ = in.push(new(big.Int).Xor(a, b))
		case 0x19: // NOT
			a, _ := in.pop()
			_ = in.push(new(big.Int).Xor(MaxUint256, a))
		case 0x1A: // BYTE: 頂 i，次 x
			i, _ := in.pop()
			x, _ := in.pop()
			r := new(big.Int)
			ii := i.Uint64()
			if ii < 32 {
				shift := uint(248 - ii*8)
				r.And(new(big.Int).Rsh(x, shift), big.NewInt(0xFF))
			}
			_ = in.push(r)
		case 0x1B: // SHL: 頂 shift，次 value
			shift, _ := in.pop()
			value, _ := in.pop()
			r := new(big.Int)
			if shift.Cmp(big.NewInt(256)) < 0 {
				r.And(new(big.Int).Lsh(value, uint(shift.Uint64())), MaxUint256)
			}
			_ = in.push(r)
		case 0x1C: // SHR
			shift, _ := in.pop()
			value, _ := in.pop()
			r := new(big.Int)
			if shift.Cmp(big.NewInt(256)) < 0 {
				r.Rsh(value, uint(shift.Uint64()))
			}
			_ = in.push(r)
		case 0x1D: // SAR
			shift, _ := in.pop()
			value, _ := in.pop()
			sv := ToSigned(value)
			var r *big.Int
			if shift.Cmp(big.NewInt(256)) >= 0 {
				r = big.NewInt(0)
				if sv.Sign() < 0 {
					r = big.NewInt(-1)
				}
			} else {
				sr := sv.Rsh(sv, uint(shift.Uint64()))
				r = FromSigned(sr)
			}
			_ = in.push(r)

		// ---- 0x20 SHA3 ----
		case 0x20:
			offset, _ := in.pop()
			size, _ := in.pop()
			off, sz := u64(offset), u64(size)
			if err := in.memoryExtend(off, sz); err != nil {
				return failResult(in, err)
			}
			_ = in.push(BytesToInt(Keccak256(in.memory[off : off+sz])))

		// ---- 0x3 環境 ----
		case 0x30: // ADDRESS
			_ = in.push(addrToInt(in.ctx.Address))
		case 0x31: // BALANCE
			a, _ := in.pop()
			_ = in.push(in.world.GetBalance(a))
		case 0x32: // ORIGIN
			_ = in.push(addrToInt(in.ctx.Origin))
		case 0x33: // CALLER
			_ = in.push(addrToInt(in.ctx.Caller))
		case 0x34: // CALLVALUE
			_ = in.push(in.ctx.CallValue)
		case 0x35: // CALLDATALOAD
			offBig, _ := in.pop()
			off := u64(offBig)
			chunk := make([]byte, WordByteLen)
			for i := uint64(0); i < WordByteLen; i++ {
				if off+i < uint64(len(calldata)) {
					chunk[i] = calldata[off+i]
				}
			}
			_ = in.push(BytesToInt(chunk))
		case 0x36: // CALLDATASIZE
			_ = in.push(big.NewInt(int64(len(calldata))))
		case 0x37: // CALLDATACOPY: dest, offset, size（棧頂 dest）
			dest, _ := in.pop()
			offset, _ := in.pop()
			size, _ := in.pop()
			dst, off, sz := u64(dest), u64(offset), u64(size)
			if err := in.memoryExtend(dst, sz); err != nil {
				return failResult(in, err)
			}
			for i := uint64(0); i < sz; i++ {
				if off+i < uint64(len(calldata)) {
					in.memory[dst+i] = calldata[off+i]
				}
			}
		case 0x38: // CODESIZE
			_ = in.push(big.NewInt(int64(len(codeBytes))))
		case 0x39: // CODECOPY: dest, offset, size（棧頂 dest）
			dest, _ := in.pop()
			offset, _ := in.pop()
			size, _ := in.pop()
			dst, off, sz := u64(dest), u64(offset), u64(size)
			if err := in.memoryExtend(dst, sz); err != nil {
				return failResult(in, err)
			}
			for i := uint64(0); i < sz; i++ {
				if off+i < uint64(len(codeBytes)) {
					in.memory[dst+i] = codeBytes[off+i]
				}
			}
		case 0x3A: // GASPRICE
			_ = in.push(in.ctx.GasPrice)
		case 0x3D: // RETURNDATASIZE
			_ = in.push(big.NewInt(int64(len(in.lastReturnData))))

		// ---- 0x4 區塊 ----
		case 0x40: // BLOCKHASH（簡化 0）
			_, _ = in.pop()
			_ = in.push(big.NewInt(0))
		case 0x41: // COINBASE
			_ = in.push(addrToInt(in.ctx.BlockCoinbase))
		case 0x42: // TIMESTAMP
			_ = in.push(big.NewInt(in.ctx.BlockTimestamp))
		case 0x43: // NUMBER
			_ = in.push(big.NewInt(in.ctx.BlockNumber))
		case 0x44: // DIFFICULTY
			_ = in.push(in.ctx.BlockDifficulty)
		case 0x45: // GASLIMIT
			_ = in.push(big.NewInt(int64(in.ctx.BlockGasLimit)))
		case 0x46: // CHAINID
			_ = in.push(in.ctx.ChainID)
		case 0x47: // SELFBALANCE
			_ = in.push(in.world.GetBalance(in.ctx.Address))
		case 0x48: // BASEFEE
			_ = in.push(in.ctx.BaseFee)

		// ---- 0x5 存儲/執行 ----
		case 0x50: // POP
			_, _ = in.pop()
		case 0x51: // MLOAD
			offBig, _ := in.pop()
			v, err := in.memoryLoad(u64(offBig))
			if err != nil {
				return failResult(in, err)
			}
			_ = in.push(v)
		case 0x52: // MSTORE: offset, value（棧頂 offset）
			offBig, _ := in.pop()
			value, _ := in.pop()
			if err := in.memoryStore(u64(offBig), value); err != nil {
				return failResult(in, err)
			}
		case 0x53: // MSTORE8: offset, value
			offBig, _ := in.pop()
			value, _ := in.pop()
			if err := in.memoryStoreByte(u64(offBig), byte(value.Uint64())); err != nil {
				return failResult(in, err)
			}
		case 0x54: // SLOAD
			k, _ := in.pop()
			_ = in.push(in.world.GetStorage(in.ctx.Address, k))
		case 0x55: // SSTORE: key, value（棧頂 key）
			if in.static {
				return failResult(in, ErrStaticState)
			}
			key, _ := in.pop()
			value, _ := in.pop()
			in.world.SetStorage(in.ctx.Address, key, value)
		case 0x56: // JUMP
			dest, _ := in.pop()
			d, ok := validJump(codeBytes, dest)
			if !ok {
				return failResult(in, ErrInvalidJump)
			}
			in.pc = d
			advance = false
		case 0x57: // JUMPI: dest, cond（棧頂 dest）
			dest, _ := in.pop()
			cond, _ := in.pop()
			if cond.Sign() != 0 {
				d, ok := validJump(codeBytes, dest)
				if !ok {
					return failResult(in, ErrInvalidJump)
				}
				in.pc = d
				advance = false
			}
		case 0x58: // PC
			_ = in.push(big.NewInt(int64(in.pc)))
		case 0x59: // MSIZE
			_ = in.push(big.NewInt(int64(len(in.memory))))
		case 0x5A: // GAS
			_ = in.push(big.NewInt(int64(in.remainingGas())))
		case 0x5B: // JUMPDEST
			// 無操作

		// ---- PUSH ----
		case 0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67,
			0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x6F,
			0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77,
			0x78, 0x79, 0x7A, 0x7B, 0x7C, 0x7D, 0x7E, 0x7F:
			n := int(op - 0x5F)
			vb := make([]byte, n)
			for i := 0; i < n; i++ {
				if in.pc+1+i < len(codeBytes) {
					vb[i] = codeBytes[in.pc+1+i]
				}
			}
			if err := in.push(BytesToInt(vb)); err != nil {
				return failResult(in, err)
			}
			in.pc += n

		// ---- DUP ----
		case 0x80, 0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87,
			0x88, 0x89, 0x8A, 0x8B, 0x8C, 0x8D, 0x8E, 0x8F:
			if err := in.dup(int(op - 0x7F)); err != nil {
				return failResult(in, err)
			}

		// ---- SWAP ----
		case 0x90, 0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97,
			0x98, 0x99, 0x9A, 0x9B, 0x9C, 0x9D, 0x9E, 0x9F:
			if err := in.swap(int(op - 0x8F)); err != nil {
				return failResult(in, err)
			}

		// ---- LOG ----
		case 0xA0, 0xA1, 0xA2, 0xA3, 0xA4:
			if in.static {
				return failResult(in, ErrStaticState)
			}
			n := int(op - 0xA0)
			offset, _ := in.pop()
			size, _ := in.pop()
			popped := make([]*big.Int, n)
			for i := 0; i < n; i++ {
				popped[i], _ = in.pop()
			}
			off, sz := u64(offset), u64(size)
			if err := in.memoryExtend(off, sz); err != nil {
				return failResult(in, err)
			}
			topics := make([]string, 0, n)
			if n > 0 {
				topics = append(topics, hex.EncodeToString(IntToBytes(popped[n-1], WordByteLen)))
				for i := n - 2; i >= 0; i-- {
					topics = append(topics, hex.EncodeToString(IntToBytes(popped[i], WordByteLen)))
				}
			}
			in.ctx.Logs = append(in.ctx.Logs, LogEntry{
				Address: in.ctx.Address, Topics: topics,
				Data: hex.EncodeToString(in.memory[off : off+sz]),
			})

		// ---- 系統 ----
		case 0xF3: // RETURN: offset, size（棧頂 offset）
			offset, _ := in.pop()
			size, _ := in.pop()
			off, sz := u64(offset), u64(size)
			if err := in.memoryExtend(off, sz); err != nil {
				return failResult(in, err)
			}
			in.ctx.ReturnData = append([]byte(nil), in.memory[off:off+sz]...)
			in.ctx.Stopped = true
			return in.finish(false)

		case 0xFD: // REVERT: offset, size
			offset, _ := in.pop()
			size, _ := in.pop()
			off, sz := u64(offset), u64(size)
			if err := in.memoryExtend(off, sz); err != nil {
				return failResult(in, err)
			}
			in.ctx.ReturnData = append([]byte(nil), in.memory[off:off+sz]...)
			in.ctx.Reverted = true
			in.ctx.RevertReason = hex.EncodeToString(in.ctx.ReturnData)
			return in.finish(true)

		case 0xFE: // INVALID
			return failResult(in, ErrInvalidOpcode)

		case 0xFF: // SELFDESTRUCT
			if in.static {
				return failResult(in, ErrStaticState)
			}
			beneficiary, _ := in.pop()
			remaining := in.world.GetBalance(in.ctx.Address)
			if remaining.Sign() > 0 {
				in.world.Transfer(in.ctx.Address, beneficiary, remaining)
			}
			in.ctx.SelfDestructed = true
			in.ctx.Stopped = true
			return in.finish(false)

		case 0xF0: // CREATE: value, offset, size（棧頂 value）
			if in.static {
				return failResult(in, ErrStaticState)
			}
			value, _ := in.pop()
			offset, _ := in.pop()
			size, _ := in.pop()
			addr, err := in.createContract(value, u64(offset), u64(size), nil)
			if err != nil {
				return failResult(in, err)
			}
			_ = in.push(addr)

		case 0xF5: // CREATE2: value, offset, size, salt
			if in.static {
				return failResult(in, ErrStaticState)
			}
			value, _ := in.pop()
			offset, _ := in.pop()
			size, _ := in.pop()
			salt, _ := in.pop()
			addr, err := in.createContract(value, u64(offset), u64(size), salt)
			if err != nil {
				return failResult(in, err)
			}
			_ = in.push(addr)

		case 0xF1: // CALL
			res := in.messageCallOp(CallKindCall)
			in.writeCallOutput(res.outOffset, res.outSize)
			_ = in.push(boolBig(res.ok))
		case 0xF2: // CALLCODE
			res := in.messageCallOp(CallKindCallCode)
			in.writeCallOutput(res.outOffset, res.outSize)
			_ = in.push(boolBig(res.ok))
		case 0xF4: // DELEGATECALL
			res := in.messageCallOp(CallKindDelegate)
			in.writeCallOutput(res.outOffset, res.outSize)
			_ = in.push(boolBig(res.ok))
		case 0xFA: // STATICCALL
			res := in.messageCallOp(CallKindStatic)
			in.writeCallOutput(res.outOffset, res.outSize)
			_ = in.push(boolBig(res.ok))

		default:
			return failResult(in, ErrInvalidOpcode)
		}

		if advance {
			in.pc++
		}
	}

	// 代碼結束（無顯式 STOP/RETURN）。
	in.ctx.Stopped = true
	return in.finish(false)
}

func boolBig(v bool) *big.Int {
	if v {
		return big.NewInt(1)
	}
	return big.NewInt(0)
}

func validJump(code []byte, dest *big.Int) (int, bool) {
	d := dest.Uint64()
	if d >= uint64(len(code)) || code[d] != 0x5B {
		return 0, false
	}
	return int(d), true
}

// finish 組裝正常/回滾結束結果。
func (in *Interpreter) finish(wasRevert bool) *ExecutionResult {
	return &ExecutionResult{
		Success:        !wasRevert && !in.ctx.Reverted,
		GasUsed:        in.gasUsed,
		ReturnData:     in.ctx.ReturnData,
		Stack:          in.stack,
		Logs:           in.ctx.Logs,
		Reverted:       wasRevert || in.ctx.Reverted,
		RevertReason:   in.ctx.RevertReason,
		SelfDestructed: in.ctx.SelfDestructed,
	}
}
