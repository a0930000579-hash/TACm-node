package vm

import (
	"bytes"
	"math/big"
	"testing"
)

func newTestVM(gas uint64) (*Interpreter, *WorldState) {
	world := NewWorldState()
	ctx := NewContext()
	if gas > 0 {
		ctx.Gas = gas
	}
	return NewInterpreter(ctx, world, 0, false), world
}

// appendReturn 把「棧頂值 → 內存0 → RETURN32」的尾碼加到 code。
func appendReturn(code []byte) []byte {
	return append(code,
		0x60, 0x00, // PUSH0 offset
		0x52,       // MSTORE
		0x60, 0x20, // PUSH32 size
		0x60, 0x00, // PUSH0 offset
		0xF3) // RETURN
}

func retInt(r *ExecutionResult) *big.Int { return BytesToInt(r.ReturnData) }

func TestArithmetic(t *testing.T) {
	cases := []struct {
		name string
		code []byte
		want int64
	}{
		{"add", []byte{0x60, 0x03, 0x60, 0x05, 0x01}, 8},  // 3+5
		{"sub", []byte{0x60, 0x0A, 0x60, 0x03, 0x03}, 7},  // 10-3
		{"mul", []byte{0x60, 0x06, 0x60, 0x07, 0x02}, 42}, // 6*7
		{"div", []byte{0x60, 0x14, 0x60, 0x04, 0x04}, 5},  // 20/4
		{"mod", []byte{0x60, 0x11, 0x60, 0x03, 0x06}, 2},  // 17%3
		{"exp", []byte{0x60, 0x02, 0x60, 0x03, 0x0A}, 8},  // 2^3
	}
	for _, c := range cases {
		in, _ := newTestVM(0)
		r := in.Execute(appendReturn(c.code), nil)
		if !r.Success || retInt(r).Cmp(big.NewInt(c.want)) != 0 {
			t.Errorf("%s: success=%v got=%s want=%d err=%s",
				c.name, r.Success, retInt(r), c.want, r.Err)
		}
	}
}

func TestComparisons(t *testing.T) {
	// 3 < 5 → 1
	code := []byte{0x60, 0x03, 0x60, 0x05, 0x10} // LT: b=5(頂),a=3 → a<b
	in, _ := newTestVM(0)
	r := in.Execute(appendReturn(code), nil)
	if retInt(r).Cmp(big.NewInt(1)) != 0 {
		t.Errorf("LT 應為1, got=%s", retInt(r))
	}
	// EQ 5==5 →1
	code2 := []byte{0x60, 0x05, 0x60, 0x05, 0x14}
	in2, _ := newTestVM(0)
	r2 := in2.Execute(appendReturn(code2), nil)
	if retInt(r2).Cmp(big.NewInt(1)) != 0 {
		t.Errorf("EQ 應為1")
	}
}

func TestStorage(t *testing.T) {
	code := []byte{
		0x60, 0x2A, // PUSH42
		0x60, 0x00, // PUSH0 key
		0x55,       // SSTORE
		0x60, 0x00, // PUSH0
		0x54, // SLOAD
	}
	in, _ := newTestVM(0)
	r := in.Execute(appendReturn(code), nil)
	if !r.Success || retInt(r).Cmp(big.NewInt(42)) != 0 {
		t.Errorf("SLOAD 應42 got=%s", retInt(r))
	}
}

func TestJump(t *testing.T) {
	code := []byte{
		0x60, 0x06, // PUSH6 dest
		0x56,       // JUMP → pc6
		0x5B,       // JUMPDEST pc3
		0x60, 0x63, // PUSH99 (dead) pc4-5
		0x5B,       // JUMPDEST pc6
		0x60, 0x07, // PUSH7
	}
	in, _ := newTestVM(0)
	r := in.Execute(appendReturn(code), nil)
	if !r.Success || retInt(r).Cmp(big.NewInt(7)) != 0 {
		t.Errorf("跳轉後應7 got=%s err=%s", retInt(r), r.Err)
	}
}

func TestRevert(t *testing.T) {
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xFD}
	in, _ := newTestVM(0)
	r := in.Execute(code, nil)
	if r.Success || !r.Reverted {
		t.Error("應回滾")
	}
}

func TestOutOfGas(t *testing.T) {
	in, _ := newTestVM(2) // PUSH 需 3
	r := in.Execute([]byte{0x60, 0x01}, nil)
	if r.Success || r.Err == "" {
		t.Error("gas 不足應失敗")
	}
}

func TestLog(t *testing.T) {
	code := []byte{
		0x60, 0xAA, // PUSH 0xaa
		0x60, 0x00, 0x52, // MSTORE @0
		0x60, 0x20, 0x60, 0x00,
		0xA0, // LOG0(0,32)
		0x00, // STOP
	}
	in, _ := newTestVM(0)
	r := in.Execute(code, nil)
	if !r.Success || len(r.Logs) != 1 {
		t.Fatalf("應有1條日誌, success=%v logs=%d", r.Success, len(r.Logs))
	}
	if r.Logs[0].Address != in.ctx.Address {
		t.Error("日誌地址錯誤")
	}
}

func TestStaticState(t *testing.T) {
	world := NewWorldState()
	ctx := NewContext()
	in := NewInterpreter(ctx, world, 0, true) // static
	code := []byte{0x60, 0x01, 0x60, 0x00, 0x55}
	r := in.Execute(code, nil)
	if r.Success {
		t.Error("靜態上下文 SSTORE 應失敗")
	}
}

// runtimeReturn42 返回固定 42 的運行時字節碼。
func runtimeReturn42() []byte {
	return []byte{0x60, 0x2A, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3}
}

// initFor 構造部署字節碼：CODECOPY runtime 並 RETURN。
func initFor(runtime []byte) []byte {
	rl := len(runtime)
	initLen := 12
	init := []byte{
		0x60, byte(rl), // PUSH size
		0x60, byte(initLen), // PUSH src offset
		0x60, 0x00, // PUSH dest
		0x39, // CODECOPY
		0x60, byte(rl),
		0x60, 0x00,
		0xF3,
	}
	return append(init, runtime...)
}

func TestContractDeployAndCall(t *testing.T) {
	world := NewWorldState()
	runtime := runtimeReturn42()
	deployCode := initFor(runtime)

	// 部署。
	dctx := NewContext()
	dctx.Address = "0x00000000000000000000000000000000000000a0"
	dep := NewInterpreter(dctx, world, 0, false)
	dr := dep.Execute(deployCode, nil)
	if !dr.Success || !bytes.Equal(dr.ReturnData, runtime) {
		t.Fatalf("部署應返回 runtime, success=%v", dr.Success)
	}

	// 存代碼並調用。
	addr := "0x00000000000000000000000000000000000000c0"
	world.SetCode(addr, dr.ReturnData)

	cctx := NewContext()
	cctx.Address = addr
	cctx.Code = world.GetCode(addr)
	call := NewInterpreter(cctx, world, 0, false)
	cr := call.Execute(nil, nil)
	if !cr.Success || retInt(cr).Cmp(big.NewInt(42)) != 0 {
		t.Errorf("調用應返回42 got=%s err=%s", retInt(cr), cr.Err)
	}
}

func TestCreateOpcode(t *testing.T) {
	runtime := runtimeReturn42()
	initFull := initFor(runtime) // 21 字節
	if len(initFull) > 32 {
		t.Fatalf("測試 init 超過32: %d", len(initFull))
	}
	init32 := make([]byte, 32)
	copy(init32, initFull)

	factory := []byte{
		0x7F, // PUSH32
	}
	factory = append(factory, init32...)
	factory = append(factory,
		0x60, 0x00, 0x52, // MSTORE init@0
		0x60, byte(len(initFull)), // size
		0x60, 0x00, // offset
		0x60, 0x00, // value
		0xF0,             // CREATE
		0x60, 0x00, 0x52, // 把地址 MSTORE@0
		0x60, 0x20, 0x60, 0x00, 0xF3) // RETURN addr

	in, world := newTestVM(0)
	r := in.Execute(factory, nil)
	if !r.Success {
		t.Fatalf("CREATE 失敗: %s", r.Err)
	}
	newAddr := NormalizeAddress(retInt(r))
	if newAddr == ZeroAddress {
		t.Fatal("新合約地址不應為0")
	}
	if !bytes.Equal(world.GetCode(newAddr), runtime) {
		t.Error("新地址應存有 runtime 代碼")
	}
}

// runtimeCounter 為每次調用把 slot0 +1 並返回新值的合約。
func runtimeCounter() []byte {
	code := []byte{
		0x60, 0x00, 0x54, // SLOAD slot0
		0x60, 0x01, 0x01, // +1
		0x60, 0x00, 0x55, // SSTORE slot0（key0 頂、value 次）
		0x60, 0x00, 0x54, // 再 SLOAD 返回
	}
	return appendReturn(code)
}
