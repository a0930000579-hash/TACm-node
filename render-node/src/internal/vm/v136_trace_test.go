package vm

import (
	"strings"
	"testing"
)

// v136：執行級 call-frame 捕獲——CALL/create 產生正確 trace 樹。

// TestTraceCallCapture：A CALL B（B=STOP）→ Trace 含 1 個 call frame。
func TestTraceCallCapture(t *testing.T) {
	ix, world := newTestVM(200000)
	a := "0x" + strings.Repeat("aa", 20)
	b := "0x" + strings.Repeat("bb", 20)
	world.SetCode(b, []byte{0x00}) // STOP
	ix.ctx.Address = a
	// PUSH outSize(0) outOff(0) inSize(0) inOff(0) value(0) to(B) gas(0xffff) CALL
	code := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00,
		0x73}
	code = append(code, mustAddr(b)...)
	code = append(code, 0x61, 0xff, 0xff, 0xF1)
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("CALL fail: %v", r.Err)
	}
	if len(ix.ctx.Trace) != 1 {
		t.Fatalf("Trace 應 1 frame, 得 %d", len(ix.ctx.Trace))
	}
	f := ix.ctx.Trace[0]
	if f.Type != "call" || f.From != a || f.To != b {
		t.Fatalf("frame 不符: %+v", f)
	}
	if f.Parent != -1 || f.Sibling != 0 {
		t.Fatalf("根 frame 屬性不符: %+v", f)
	}
	if f.Reverted {
		t.Fatal("B STOP 不應 reverted")
	}
}

// TestTraceCreateCapture：CREATE（無 init 內容）→ Trace 含 create frame。
func TestTraceCreateCapture(t *testing.T) {
	ix, world := newTestVM(200000)
	a := "0x" + strings.Repeat("aa", 20)
	world.Ensure(a)
	ix.ctx.Address = a
	// PUSH size(0) offset(0) value(0) CREATE（value 頂）
	code := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0xF0}
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("CREATE fail: %v", r.Err)
	}
	if len(ix.ctx.Trace) != 1 || ix.ctx.Trace[0].Type != "create" {
		t.Fatalf("Trace create frame 不符: %+v", ix.ctx.Trace)
	}
	cf := ix.ctx.Trace[0]
	if cf.From != a || cf.To == "" {
		t.Fatalf("create frame 欄位不符: %+v", cf)
	}
	if cf.Reverted {
		t.Fatal("空 init 不應 reverted")
	}
}

// TestTraceNested：A CALL B、B 內再 CALL C → 2 frames 且 parent 鏈結正確。
func TestTraceNested(t *testing.T) {
	ix, world := newTestVM(200000)
	a := "0x" + strings.Repeat("aa", 20)
	b := "0x" + strings.Repeat("bb", 20)
	c := "0x" + strings.Repeat("cc", 20)
	world.SetCode(c, []byte{0x00})
	// B code：CALL C（同 A 的 CALL 組裝，to=C）
	bcode := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00,
		0x73}
	bcode = append(bcode, mustAddr(c)...)
	bcode = append(bcode, 0x61, 0xff, 0xff, 0xF1)
	world.SetCode(b, bcode)
	ix.ctx.Address = a
	acode := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00,
		0x73}
	acode = append(acode, mustAddr(b)...)
	acode = append(acode, 0x61, 0xff, 0xff, 0xF1)
	r := ix.Execute(acode, nil)
	if !r.Success {
		t.Fatalf("nested CALL fail: %v", r.Err)
	}
	if len(ix.ctx.Trace) != 2 {
		t.Fatalf("Trace 應 2 frames, 得 %d: %+v", len(ix.ctx.Trace), ix.ctx.Trace)
	}
	f1, f2 := ix.ctx.Trace[0], ix.ctx.Trace[1]
	if f1.Type != "call" || f1.To != b || f1.Parent != -1 {
		t.Fatalf("frame1 不符: %+v", f1)
	}
	if f2.Type != "call" || f2.To != c || f2.Parent != 0 || f2.Sibling != 0 {
		t.Fatalf("frame2（B→C 應為 frame1 子節點）不符: %+v", f2)
	}
}
