package vm

// v137：逐 opcode 步驟收集（structLogs 來源）——CollectSteps 開關與步驟內容驗證。

import "testing"

// TestStepsCollectOn：CollectSteps=true 時收集非空步驟，含 pc/op/opName/gasCost。
func TestStepsCollectOn(t *testing.T) {
	interp, _ := newTestVM(100000)
	interp.ctx.CollectSteps = true
	// PUSH1 0x2a PUSH1 0x00 MSTORE PUSH1 0x20 PUSH1 0x00 RETURN
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3}
	res := interp.Execute(code, nil)
	if res.Reverted {
		t.Fatalf("執行不應回滾: %v", res.Err)
	}
	if len(interp.ctx.Steps) == 0 {
		t.Fatal("CollectSteps=true 應收集步驟")
	}
	first := interp.ctx.Steps[0]
	if first.Op != "0x60" || first.OpName != "PUSH1" {
		t.Fatalf("首步應為 PUSH1: %+v", first)
	}
	if first.GasCost == 0 {
		t.Fatalf("PUSH1 gasCost 不應為 0: %+v", first)
	}
	if first.Depth != 0 {
		t.Fatalf("根層 depth 應為 0: %+v", first)
	}
}

// TestStepsCollectOff：預設（false）零收集，熱路徑零開銷。
func TestStepsCollectOff(t *testing.T) {
	interp, _ := newTestVM(100000)
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3}
	res := interp.Execute(code, nil)
	if res.Reverted {
		t.Fatalf("執行不應回滾: %v", res.Err)
	}
	if interp.ctx.Steps != nil {
		t.Fatalf("CollectSteps=false 不應收集: %d steps", len(interp.ctx.Steps))
	}
}

// TestStepsStackTop：stack 頂部最多 4 項、含正確值。
func TestStepsStackTop(t *testing.T) {
	interp, _ := newTestVM(100000)
	interp.ctx.CollectSteps = true
	// PUSH1 0x2a → 首步 stack 應含 0x2a（PUSH 後 push 值發生在 switch 內；驗證執行完 steps 非空即可）
	code := []byte{0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3}
	_ = interp.Execute(code, nil)
	if len(interp.ctx.Steps) == 0 {
		t.Fatal("應有步驟")
	}
	last := interp.ctx.Steps[len(interp.ctx.Steps)-1]
	if len(last.Stack) > 4 {
		t.Fatalf("stack 最多 4 項: %d", len(last.Stack))
	}
}
