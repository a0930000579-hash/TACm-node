package vm

import (
	"bytes"
	"errors"
	"math/big"
	"strings"
	"testing"
)

// v134：主流 EVM opcode 補齊（Shanghai/Cancun/基礎 EXT 系列）。
// EXTCODESIZE / EXTCODECOPY / EXTCODEHASH / PUSH0 / TLOAD / TSTORE / MCOPY / BLOBBASEFEE。

func TestExtCodeSize(t *testing.T) {
	ix, world := newTestVM(200000)
	addr := "0x" + strings.Repeat("11", 20)
	world.SetCode(addr, []byte{0x60, 0x01})
	// PUSH20 addr; EXTCODESIZE; 回傳
	code := append([]byte{0x73}, mustAddr(addr)...)
	code = append(code, 0x3B)
	code = appendReturn(code)
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("EXTCODESIZE fail: %v", r.Err)
	}
	if retInt(r).Int64() != 2 {
		t.Fatalf("EXTCODESIZE 不符: %d want 2", retInt(r).Int64())
	}
}

func TestExtCodeCopy(t *testing.T) {
	ix, world := newTestVM(200000)
	addr := "0x" + strings.Repeat("22", 20)
	src := []byte{0xAA, 0xBB, 0xCC}
	world.SetCode(addr, src)
	// size 底、offset、address 頂（EVM pop：address, offset, size）
	code := append([]byte{0x60, 0x03}, 0x60, 0x00) // PUSH1 3(size); PUSH1 0(offset)
	code = append(code, 0x73)                      // PUSH20 address（頂）
	code = append(code, mustAddr(addr)...)
	code = append(code, 0x3C)
	code = append(code, 0x60, 0x03, 0x60, 0x00, 0xF3) // RETURN(0,3)：size 底、offset 頂
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("EXTCODECOPY fail: %v", r.Err)
	}
	if !bytes.Equal(r.ReturnData, src) {
		t.Fatalf("EXTCODECOPY 不符: %x", r.ReturnData)
	}
	// 拷超出 code 長度 → 填 0
	code2 := append([]byte{0x60, 0x05}, 0x60, 0x00) // size=5; offset=0
	code2 = append(code2, 0x73)
	code2 = append(code2, mustAddr(addr)...)
	code2 = append(code2, 0x3C)
	code2 = append(code2, 0x60, 0x05, 0x60, 0x00, 0xF3)
	r2 := ix.Execute(code2, nil)
	if !r2.Success || !bytes.Equal(r2.ReturnData, []byte{0xAA, 0xBB, 0xCC, 0, 0}) {
		t.Fatalf("EXTCODECOPY 填 0 不符: %x", r2.ReturnData)
	}
}

func TestExtCodeHash(t *testing.T) {
	ix, world := newTestVM(200000)
	// 有碼
	addr := "0x" + strings.Repeat("33", 20)
	world.SetCode(addr, []byte{0x60, 0x00})
	want := Keccak256([]byte{0x60, 0x00})
	code := append([]byte{0x73}, mustAddr(addr)...)
	code = append(code, 0x3F)
	code = appendReturn(code)
	r := ix.Execute(code, nil)
	if !r.Success || !bytes.Equal(IntToBytes(retInt(r), 32), want) {
		t.Fatalf("EXTCODEHASH 不符: %x want %x", retInt(r).Bytes(), want)
	}
	// 無碼 → 0
	empty := "0x" + strings.Repeat("44", 20)
	code2 := append([]byte{0x73}, mustAddr(empty)...)
	code2 = append(code2, 0x3F)
	code2 = appendReturn(code2)
	r2 := ix.Execute(code2, nil)
	if !r2.Success || retInt(r2).Sign() != 0 {
		t.Fatalf("EXTCODEHASH 無碼應為 0: %v", retInt(r2))
	}
}

func TestPush0(t *testing.T) {
	ix, _ := newTestVM(200000)
	// PUSH0 PUSH0 STOP → 執行成功且 gas 僅 2+2
	r := ix.Execute([]byte{0x5F, 0x5F, 0x00}, nil)
	if !r.Success {
		t.Fatalf("PUSH0 fail: %v", r.Err)
	}
	if len(r.Stack) != 2 || r.Stack[0].Sign() != 0 || r.Stack[1].Sign() != 0 {
		t.Fatalf("PUSH0 棧不符: %v", r.Stack)
	}
}

func TestTransientStorage(t *testing.T) {
	ix, _ := newTestVM(200000)
	// TSTORE(7, 42) → TLOAD(7) → 回傳
	code := []byte{0x60, 0x2A, 0x60, 0x07, 0x5D} // value=42, key=7, TSTORE
	code = append(code, 0x60, 0x07, 0x5C)        // key=7, TLOAD
	code = appendReturn(code)
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("TSTORE/TLOAD fail: %v", r.Err)
	}
	if retInt(r).Int64() != 42 {
		t.Fatalf("瞬態讀寫不符: %d want 42", retInt(r).Int64())
	}
	// 新交易（ResetTransient）→ 歸零
	ix.ctx.ResetTransient()
	code2 := []byte{0x60, 0x07, 0x5C}
	code2 = appendReturn(code2)
	r2 := ix.Execute(code2, nil)
	if !r2.Success || retInt(r2).Sign() != 0 {
		t.Fatalf("ResetTransient 後應為 0: %v", retInt(r2))
	}
	// static 模式 TSTORE 拒絕
	ctx := NewContext()
	ctx.Gas = 200000
	static := NewInterpreter(ctx, NewWorldState(), 0, true)
	r3 := static.Execute([]byte{0x60, 0x2A, 0x60, 0x07, 0x5D}, nil)
	if r3.Success {
		t.Fatal("static 模式 TSTORE 應拒絕")
	}
}

func TestMCopy(t *testing.T) {
	ix, _ := newTestVM(200000)
	// MSTORE8(0, 0xAB) → MCOPY(dst=2, src=0, size=2) → RETURN(2,2)
	code := []byte{0x60, 0xAB, 0x60, 0x00, 0x53} // MSTORE8(0, AB)
	code = append(code, 0x60, 0x02, 0x60, 0x00, 0x60, 0x02, 0x5E) // len=2(底), src=0, dst=2(頂), MCOPY
	code = append(code, 0x60, 0x02, 0x60, 0x02, 0xF3)            // RETURN(2,2)：size 底、offset 頂
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("MCOPY fail: %v", r.Err)
	}
	if !bytes.Equal(r.ReturnData, []byte{0xAB, 0x00}) {
		t.Fatalf("MCOPY 不符: %x", r.ReturnData)
	}
}

func TestBlobBaseFee(t *testing.T) {
	ix, _ := newTestVM(200000)
	r := ix.Execute(append([]byte{0x4A}, 0x00), nil) // BLOBBASEFEE; STOP
	if !r.Success || len(r.Stack) != 1 || r.Stack[0].Sign() != 0 {
		t.Fatalf("BLOBBASEFEE 應為 0: %v", r.Stack)
	}
}

// TestSelfDestructCleans：SELFDESTRUCT 後帳號 code/storage/餘額全清（EIP-6780 主幹）。
func TestSelfDestructCleans(t *testing.T) {
	ix, world := newTestVM(200000)
	target := "0x" + strings.Repeat("55", 20)
	benef := "0x" + strings.Repeat("66", 20)
	world.SetCode(target, []byte{0x60, 0x01})
	world.SetStorage(target, big.NewInt(1), big.NewInt(7))
	world.SetBalance(target, big.NewInt(100))
	world.SetNonce(target, 3)
	ix.ctx.Address = target // 執行上下文＝目標合約
	// PUSH20 benef; SELFDESTRUCT
	code := append([]byte{0x73}, mustAddr(benef)...)
	code = append(code, 0xFF)
	r := ix.Execute(code, nil)
	if !r.Success {
		t.Fatalf("SELFDESTRUCT fail: %v", r.Err)
	}
	if !r.SelfDestructed {
		t.Fatal("應標記 SelfDestructed")
	}
	if len(world.GetCode(target)) != 0 {
		t.Fatal("code 未清")
	}
	if v := world.GetStorage(target, big.NewInt(1)); v.Sign() != 0 {
		t.Fatal("storage 未清")
	}
	if b := world.GetBalance(target); b.Sign() != 0 {
		t.Fatal("餘額未清")
	}
	if nb := world.GetNonce(target); nb != 0 {
		t.Fatal("nonce 未清")
	}
	if b := world.GetBalance(benef); b.Int64() != 100 {
		t.Fatalf("beneficiary 未收到 100: %v", b)
	}
}

// mustAddr 20 字節地址 bytes。
func mustAddr(h string) []byte {
	b, _ := hexDecode(strings.TrimPrefix(h, "0x"))
	return b
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var v int
		if _, err := fmtSscanf(s[i*2:i*2+2], &v); err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

func fmtSscanf(hex string, v *int) (int, error) {
	n := 0
	for _, c := range hex {
		n <<= 4
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n += int(c-'A') + 10
		default:
			return 0, errors.New("bad hex")
		}
	}
	*v = n
	return 1, nil
}
