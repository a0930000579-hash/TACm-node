package vm

// M134-A：EVM Precompiles 測試（ecrecover/sha256/ripemd160/identity/VRF）。

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"

	"tacm/internal/crypto"
)

func TestM134PrecompileIdentity(t *testing.T) {
	out, matched, gas, err := runPrecompile(NewContext(), big.NewInt(4), []byte("hello world"))
	if err != nil || !matched {
		t.Fatalf("identity 應無誤且 matched: %v %v", err, matched)
	}
	if string(out) != "hello world" {
		t.Fatalf("identity 輸出錯誤: %q", out)
	}
	if gas != 15+3*1 {
		t.Fatalf("identity gas 應 18: %d", gas)
	}
}

func TestM134PrecompileSha256(t *testing.T) {
	out, matched, _, err := runPrecompile(NewContext(), big.NewInt(2), []byte("abc"))
	if err != nil || !matched {
		t.Fatalf("sha256 失敗: %v", err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if hex.EncodeToString(out) != want {
		t.Fatalf("sha256 輸出錯誤: %s", hex.EncodeToString(out))
	}
}

func TestM134PrecompileRipemd160(t *testing.T) {
	out, matched, _, err := runPrecompile(NewContext(), big.NewInt(3), []byte("abc"))
	if err != nil || !matched {
		t.Fatalf("ripemd160 失敗: %v", err)
	}
	// ripemd160("abc") = 8eb208f7e05d987a9b044a8e98c6b087f15a0bfc，left-pad 32 bytes。
	want := "0000000000000000000000008eb208f7e05d987a9b044a8e98c6b087f15a0bfc"
	if hex.EncodeToString(out) != want {
		t.Fatalf("ripemd160 輸出錯誤: %s", hex.EncodeToString(out))
	}
}

func TestM134PrecompileEcrecover(t *testing.T) {
	// 本地自洽：生成密鑰 → SignCompact → 拆 (v,r,s) → ecrecover 恢復 → 對比 hash160。
	privB := make([]byte, 32)
	privB[31] = 7
	priv, pub := btcec.PrivKeyFromBytes(privB)
	_ = priv
	want := crypto.Hash160(pub.SerializeCompressed())
	digest := []byte("01234567890123456789012345678901") // 32 bytes

	var compact []byte
	for i := 0; i < 64; i++ {
		c, err := ecdsa.SignCompact(priv, digest, true)
		if err != nil {
			t.Fatalf("SignCompact: %v", err)
		}
		if len(c) != 65 {
			t.Fatalf("compact 應 65 bytes: %d", len(c))
		}
		// 以太 ecrecover 的 v 僅提供 parity（27/28）；試兩個值皆可。
		for _, vv := range []byte{27, 28} {
			v := make([]byte, 32)
			v[31] = vv
			input := append([]byte(nil), digest...)
			input = append(input, v...)
			input = append(input, c[1:33]...)
			input = append(input, c[33:65]...)
			out, matched, _, err := runPrecompile(NewContext(), big.NewInt(1), input)
			if err != nil || !matched {
				continue
			}
			if len(out) == 32 && string(out[12:]) == string(want) {
				compact = c
				break
			}
		}
		if len(compact) == 65 {
			break
		}
		digest[31]++
	}
	if len(compact) != 65 {
		t.Fatalf("無法取得可恢復簽名")
	}
	// 再驗證一次正式路徑（v=27/28 其一必成功）輸出格式（32-byte left-padded）。
	ok := false
	for _, vv := range []byte{27, 28} {
		v := make([]byte, 32)
		v[31] = vv
		input := append([]byte(nil), digest...)
		input = append(input, v...)
		input = append(input, compact[1:33]...)
		input = append(input, compact[33:65]...)
		out, matched, _, err := runPrecompile(NewContext(), big.NewInt(1), input)
		if err == nil && matched && len(out) == 32 && string(out[12:]) == string(want) {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("正式路徑 v=27/28 皆未恢復正確地址")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return b
}

// sha256Hash160 測試用：SHA256→RIPEMD160（與 ecrecover 地址語意一致）。

// TestM134PrecompileViaContract 驗證合約 CALL 0x02（sha256 precompile）完整分派路徑。
func TestM134PrecompileViaContract(t *testing.T) {
	world := NewWorldState()
	ix := NewInterpreter(NewContext(), world, 0, false)
	// runtime：CALLDATACOPY(0,0,3) → CALL(0x02, gas=0xffff, in=mem[0:3], out=mem[0:32]) → 回 mem[0:32]
	// 60036000600037 60206000600360006000600261fffff1 600051 600052 60206000f3
	runtime := []byte{
		0x60, 0x03, 0x60, 0x00, 0x60, 0x00, 0x37,
		0x60, 0x20, 0x60, 0x00, 0x60, 0x03, 0x60, 0x00, 0x60, 0x00,
		0x60, 0x02, 0x61, 0xff, 0xff, 0xf1,
		0x60, 0x00, 0x51, 0x60, 0x00, 0x52,
		0x60, 0x20, 0x60, 0x00, 0xf3,
	}
	res := ix.Execute(runtime, []byte("abc"))
	if !res.Success {
		t.Fatalf("執行失敗: %v", res.Err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if hex.EncodeToString(res.ReturnData) != want {
		t.Fatalf("合約 CALL sha256 輸出錯誤: %s", hex.EncodeToString(res.ReturnData))
	}
}

// M151c：VRF precompile（0x09）——RANDAO 式鏈上隨機。
func TestM151cVRFDeterministic(t *testing.T) {
	ctx := NewContext()
	ctx.BlockNumber = 100
	ctx.BlockCoinbase = "tx01CoinbaseAddr"
	out1, matched, gas, err := runPrecompile(ctx, big.NewInt(9), []byte("seed-1"))
	if err != nil || !matched {
		t.Fatalf("VRF 應無誤且 matched: %v %v", err, matched)
	}
	if len(out1) != 32 {
		t.Fatalf("VRF 輸出應 32 bytes: %d", len(out1))
	}
	if gas != 30000 {
		t.Fatalf("VRF gas 應 30000: %d", gas)
	}
	// 同區塊同輸入 → 同輸出（確定性）。
	out2, _, _, _ := runPrecompile(ctx, big.NewInt(9), []byte("seed-1"))
	if hex.EncodeToString(out1) != hex.EncodeToString(out2) {
		t.Fatal("同區塊同輸入應同輸出")
	}
}

func TestM151cVRFVariesByBlockAndSeed(t *testing.T) {
	ctx := NewContext()
	ctx.BlockNumber = 100
	ctx.BlockTimestamp = 1700000000
	ctx.BlockCoinbase = "tx01CoinbaseAddr"
	base, _, _, _ := runPrecompile(ctx, big.NewInt(9), []byte("seed"))
	// 不同區塊（timestamp 不同）→ 不同輸出。
	ctx2 := NewContext()
	ctx2.BlockNumber = 101
	ctx2.BlockTimestamp = 1700000001
	ctx2.BlockCoinbase = "tx01CoinbaseAddr"
	other, _, _, _ := runPrecompile(ctx2, big.NewInt(9), []byte("seed"))
	if hex.EncodeToString(base) == hex.EncodeToString(other) {
		t.Fatal("不同區塊應不同輸出")
	}
	// 不同 seed → 不同輸出。
	other2, _, _, _ := runPrecompile(ctx, big.NewInt(9), []byte("seed-x"))
	if hex.EncodeToString(base) == hex.EncodeToString(other2) {
		t.Fatal("不同 seed 應不同輸出")
	}
	// 空輸入仍回 32 bytes（seed=0）。
	empty, _, _, _ := runPrecompile(ctx, big.NewInt(9), nil)
	if len(empty) != 32 {
		t.Fatalf("空輸入應回 32 bytes: %d", len(empty))
	}
}

// M151c：VRF 依區塊 timestamp 變化（每塊新隨機的實務來源）。
func TestM151cVRFVariesByTimestamp(t *testing.T) {
	ctx := NewContext()
	ctx.BlockNumber = 100
	ctx.BlockTimestamp = 1700000000
	ctx.BlockCoinbase = "tx01CoinbaseAddr"
	base, _, _, _ := runPrecompile(ctx, big.NewInt(9), []byte("seed"))
	ctx3 := NewContext()
	ctx3.BlockNumber = 100
	ctx3.BlockTimestamp = 1700000005
	ctx3.BlockCoinbase = "tx01CoinbaseAddr"
	ts, _, _, _ := runPrecompile(ctx3, big.NewInt(9), []byte("seed"))
	if hex.EncodeToString(base) == hex.EncodeToString(ts) {
		t.Fatal("不同 timestamp 應不同輸出")
	}
}

// M151c：VRF 非 precompile 地址不匹配。
func TestM151cVRFNonPrecompile(t *testing.T) {
	_, matched, _, _ := runPrecompile(NewContext(), big.NewInt(8), nil)
	if matched {
		t.Fatal("地址 0x08 非 precompile，不應 matched")
	}
}
