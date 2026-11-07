package vm

import (
	"encoding/hex"
	"math/big"
	"testing"
)

func hx(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// 官方 Solidity ABI 範例：f(uint256,uint32[],bytes10,bytes)
func TestEncodeArgsOfficial(t *testing.T) {
	args := []any{big.NewInt(0x123), []any{big.NewInt(0x456), big.NewInt(0x789)}, "0x31323334353637383930", "0x61626364"}
	got, err := EncodeArgs([]string{"uint256", "uint32[]", "bytes10", "bytes"}, args)
	if err != nil {
		t.Fatal(err)
	}
	want := "0000000000000000000000000000000000000000000000000000000000000123" +
		"0000000000000000000000000000000000000000000000000000000000000080" +
		"3132333435363738393000000000000000000000000000000000000000000000" +
		"00000000000000000000000000000000000000000000000000000000000000e0" +
		"0000000000000000000000000000000000000000000000000000000000000002" +
		"0000000000000000000000000000000000000000000000000000000000000456" +
		"0000000000000000000000000000000000000000000000000000000000000789" +
		"0000000000000000000000000000000000000000000000000000000000000004" +
		"6162636400000000000000000000000000000000000000000000000000000000"
	if hex.EncodeToString(got) != want {
		t.Fatalf("官方向量不符\n got=%s\nwant=%s", hex.EncodeToString(got), want)
	}
}

func TestEncodeArgsStatic(t *testing.T) {
	got, err := EncodeArgs([]string{"address", "bool", "uint8"}, []any{"0xCD2a3d9F938E13CD947Ec05AbC7FE734Df8DD826", true, 3})
	if err != nil {
		t.Fatal(err)
	}
	want := "000000000000000000000000cd2a3d9f938e13cd947ec05abc7fe734df8dd826" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000003"
	if hex.EncodeToString(got) != want {
		t.Fatalf("靜態編碼不符\n got=%s\nwant=%s", hex.EncodeToString(got), want)
	}
}

func TestEncodeArgsNegativeInt(t *testing.T) {
	got, err := EncodeArgs([]string{"int256"}, []any{big.NewInt(-1)})
	if err != nil {
		t.Fatal(err)
	}
	want := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if hex.EncodeToString(got) != want {
		t.Fatalf("int -1 二補數不符: %s", hex.EncodeToString(got))
	}
}

func TestDecodeOutputs(t *testing.T) {
	// balanceOf → uint256：回 250
	enc, err := EncodeArgs([]string{"uint256"}, []any{big.NewInt(250)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeOutputs([]string{"uint256"}, enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "250" {
		t.Fatalf("uint256 解碼不符: %v", got)
	}

	// 多型別：address + bool + string
	enc2, err := EncodeArgs([]string{"address", "bool", "string"}, []any{"0xCD2a3d9F938E13CD947Ec05AbC7FE734Df8DD826", false, "hi"})
	if err != nil {
		t.Fatal(err)
	}
	got2, err := DecodeOutputs([]string{"address", "bool", "string"}, enc2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 3 || got2[0] != "0xcd2a3d9f938e13cd947ec05abc7fe734df8dd826" || got2[1] != false || got2[2] != "hi" {
		t.Fatalf("多型別解碼不符: %v", got2)
	}

	// 動態陣列 uint256[]
	enc3, err := EncodeArgs([]string{"uint256[]"}, []any{[]any{big.NewInt(1), big.NewInt(2), big.NewInt(3)}})
	if err != nil {
		t.Fatal(err)
	}
	got3, err := DecodeOutputs([]string{"uint256[]"}, enc3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got3) != 1 {
		t.Fatalf("陣列解碼數量不符: %v", got3)
	}
	arr, ok := got3[0].([]any)
	if !ok || len(arr) != 3 || arr[0] != "1" || arr[2] != "3" {
		t.Fatalf("陣列解碼不符: %v", got3)
	}
}

func TestFuncSelector(t *testing.T) {
	// transfer(address,uint256) → 0xa9059cbb
	sel, err := FuncSelector("transfer(address,uint256)")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(sel) != "a9059cbb" {
		t.Fatalf("transfer selector 不符: %s", hex.EncodeToString(sel))
	}
	// balanceOf(address) → 0x70a08231
	sel2, err := FuncSelector("balanceOf(address)")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(sel2) != "70a08231" {
		t.Fatalf("balanceOf selector 不符: %s", hex.EncodeToString(sel2))
	}
}

func TestABIErrors(t *testing.T) {
	if _, err := EncodeArgs([]string{"uint256"}, []any{}); err == nil {
		t.Fatal("參數數量不符應報錯")
	}
	if _, err := EncodeArgs([]string{"nonsense"}, []any{1}); err == nil {
		t.Fatal("未知型別應報錯")
	}
	if _, err := EncodeArgs([]string{"address"}, []any{"0x1234"}); err == nil {
		t.Fatal("address 長度錯誤應報錯")
	}
	if _, err := DecodeOutputs([]string{"uint256"}, hx("aabb")); err == nil {
		t.Fatal("return data 不足應報錯")
	}
}
