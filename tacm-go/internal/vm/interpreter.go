package vm

import (
	"math/big"
)

// maxMemory 為單幀內存安全上限（簡化 gas 模型下防止超大分配）。
const maxMemory = 0x400_0000 // 64 MB

// Interpreter 為 TAC VM 字節碼解釋器（單幀）。
type Interpreter struct {
	ctx   *ExecutionContext
	world *WorldState

	stack  []*big.Int
	memory []byte
	pc     int

	gasUsed uint64
	depth   int
	static  bool

	lastReturnData []byte

	trace       []string
	enableTrace bool
}

func NewInterpreter(ctx *ExecutionContext, world *WorldState, depth int, isStatic bool) *Interpreter {
	return &Interpreter{ctx: ctx, world: world, depth: depth, static: isStatic}
}

func (in *Interpreter) push(x *big.Int) error {
	if len(in.stack) >= MaxStackSize {
		return ErrStackOverflow
	}
	in.stack = append(in.stack, U256(x))
	return nil
}

func (in *Interpreter) pop() (*big.Int, error) {
	if len(in.stack) == 0 {
		return nil, ErrStackUnderflow
	}
	v := in.stack[len(in.stack)-1]
	in.stack = in.stack[:len(in.stack)-1]
	return v, nil
}

func (in *Interpreter) peek(idx int) (*big.Int, error) {
	if idx >= len(in.stack) {
		return nil, ErrStackUnderflow
	}
	return in.stack[len(in.stack)-1-idx], nil
}

func (in *Interpreter) swap(n int) error {
	if n >= len(in.stack) {
		return ErrStackUnderflow
	}
	l := len(in.stack)
	in.stack[l-1], in.stack[l-1-n] = in.stack[l-1-n], in.stack[l-1]
	return nil
}

func (in *Interpreter) dup(n int) error {
	if n > len(in.stack) {
		return ErrStackUnderflow
	}
	return in.push(in.stack[len(in.stack)-n])
}

func addOverflow(a, b uint64) (uint64, bool) {
	s := a + b
	if s < a {
		return 0, true
	}
	return s, false
}

// memoryExtend 擴展內存至 offset+size 並零填充。
func (in *Interpreter) memoryExtend(offset, size uint64) error {
	if size == 0 {
		return nil
	}
	needed, overflow := addOverflow(offset, size)
	if overflow || needed > maxMemory {
		return ErrOutOfGas
	}
	if uint64(len(in.memory)) < needed {
		in.memory = append(in.memory,
			make([]byte, needed-uint64(len(in.memory)))...)
	}
	return nil
}

func (in *Interpreter) memoryLoad(offset uint64) (*big.Int, error) {
	if err := in.memoryExtend(offset, WordByteLen); err != nil {
		return nil, err
	}
	return BytesToInt(in.memory[offset : offset+WordByteLen]), nil
}

func (in *Interpreter) memoryStore(offset uint64, value *big.Int) error {
	if err := in.memoryExtend(offset, WordByteLen); err != nil {
		return err
	}
	copy(in.memory[offset:offset+WordByteLen], IntToBytes(value, WordByteLen))
	return nil
}

func (in *Interpreter) memoryStoreByte(offset uint64, value byte) error {
	if err := in.memoryExtend(offset, 1); err != nil {
		return err
	}
	in.memory[offset] = value
	return nil
}

func (in *Interpreter) consumeGas(name string) error {
	c := GasCost(name)
	in.gasUsed += c
	if in.gasUsed > in.ctx.Gas {
		return ErrOutOfGas
	}
	return nil
}

// addChildGas 累加子調用實際 gas，超限報錯。
func (in *Interpreter) addChildGas(used uint64) error {
	in.gasUsed += used
	if in.gasUsed > in.ctx.Gas {
		return ErrOutOfGas
	}
	return nil
}

// remainingGas 返回當前剩餘 gas。
func (in *Interpreter) remainingGas() uint64 {
	if in.gasUsed >= in.ctx.Gas {
		return 0
	}
	return in.ctx.Gas - in.gasUsed
}
