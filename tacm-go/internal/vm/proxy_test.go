package vm

// v133：EIP-1967 可升級代理測試——部署/查詢/升級/權限/fallback delegatecall e2e。

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// implCode：迷你 implementation runtime——calldata 前 32 bytes 寫 slot0，再回 slot0。
// 60 00 35 60 00 55 60 00 54 60 00 52 60 20 60 00 F3
func miniImplRuntime() []byte {
	return []byte{
		0x60, 0x00, 0x35, // CALLDATALOAD(0)
		0x60, 0x00, 0x55, // SSTORE slot0=value
		0x60, 0x00, 0x54, // SLOAD slot0
		0x60, 0x00, 0x52, // MSTORE@0
		0x60, 0x20, 0x60, 0x00, 0xF3, // RETURN(0,32)
	}
}

func deployProxy(t *testing.T, implAddr string) (*WorldState, []byte, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	in.ctx.Caller = creator
	res := in.Execute(ProxyInit(implAddr), nil)
	if !res.Success {
		t.Fatalf("Proxy 部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	if string(res.ReturnData) != string(ProxyRuntime()) {
		t.Fatal("部署回傳 runtime 與 ProxyRuntime() 不一致")
	}
	return world, res.ReturnData, creator
}

func queryProxy(t *testing.T, world *WorldState, runtime []byte, creator string, calldata []byte) *big.Int {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	r := ix.Execute(runtime, calldata)
	if !r.Success {
		t.Fatalf("Proxy 執行失敗: %v", r.Err)
	}
	return retInt(r)
}

// 帶 world 的執行（fallback delegatecall 需要目標 code）。
func execWithWorld(t *testing.T, world *WorldState, runtime []byte, caller string, calldata []byte) *ExecutionResult {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = caller
	return ix.Execute(runtime, calldata)
}

// toHex32 把 big.Int 轉 32 字節右對齊 hex（地址比對用）。
func toHex32(x *big.Int) string {
	b := x.Bytes()
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return hex.EncodeToString(out)
}

func TestProxyDeployAndQuery(t *testing.T) {
	impl := "0x" + strings.Repeat("22", 20)
	world, rt, creator := deployProxy(t, impl)

	// implementation() = impl
	got := queryProxy(t, world, rt, creator, ProxyImplCalldata())
	if !strings.HasSuffix(toHex32(got), strings.TrimPrefix(impl, "0x")) {
		t.Fatalf("implementation() 不符: %s", toHex32(got))
	}

	// admin() = creator
	adm := queryProxy(t, world, rt, creator, ProxyAdminCalldata())
	if !strings.HasSuffix(toHex32(adm), strings.TrimPrefix(creator, "0x")) {
		t.Fatalf("admin() 不符: %s", toHex32(adm))
	}
}

func TestProxyUpgradeOnlyAdmin(t *testing.T) {
	impl1 := "0x" + strings.Repeat("22", 20)
	impl2 := "0x" + strings.Repeat("33", 20)
	world, rt, creator := deployProxy(t, impl1)

	// 非 admin 升級 → 失敗。
	evil := "0x" + strings.Repeat("44", 20)
	r := execWithWorld(t, world, rt, evil, ProxyUpgradeToCalldata(impl2))
	if r.Success {
		t.Fatal("非 admin 升級應失敗")
	}

	// admin 升級 → 成功，implementation() 更新。
	r = execWithWorld(t, world, rt, creator, ProxyUpgradeToCalldata(impl2))
	if !r.Success {
		t.Fatalf("admin 升級失敗: %v", r.Err)
	}
	got := queryProxy(t, world, rt, creator, ProxyImplCalldata())
	gotHex := toHex32(got)
	if !strings.HasSuffix(gotHex, strings.TrimPrefix(impl2, "0x")) {
		t.Fatalf("升級後 implementation() 不符: %x", got)
	}
}

func TestProxyFallbackDelegateCall(t *testing.T) {
	implAddr := "0x" + strings.Repeat("22", 20)
	implAddrRaw := strings.TrimPrefix(implAddr, "0x")

	// 部署 proxy（storage 含 implSlot/adminSlot）並在**同一 world** 註冊 impl code。
	world, rt, creator := deployProxy(t, implAddr)
	world.SetCode(implAddrRaw, miniImplRuntime())

	// 透過 proxy 呼叫 impl 的「寫 slot0」介面（calldata=64 bytes value）。
	val := new(big.Int).Lsh(big.NewInt(0xdead), 32) // 高位避免截斷測試
	valueBytes := leftPad20(val.Bytes(), 32)
	calldata := append(valueBytes, 0xAA) // 任意尾巴，impl 只讀前 32
	r := execWithWorld(t, world, rt, creator, calldata)
	if !r.Success {
		t.Fatalf("fallback delegatecall 失敗: %v", r.Err)
	}
	// 回傳 = storage[slot0]（impl 在代理 storage 上下文寫入）
	got := retInt(r)
	gotBytes := leftPad20(got.Bytes(), 32)
	if string(gotBytes) != string(valueBytes) {
		t.Fatalf("delegatecall 回傳不符: %x", got)
	}
}

func TestProxyChangeAdmin(t *testing.T) {
	impl := "0x" + strings.Repeat("22", 20)
	world, rt, creator := deployProxy(t, impl)
	newAdmin := "0x" + strings.Repeat("55", 20)

	r := execWithWorld(t, world, rt, creator, ProxyChangeAdminCalldata(newAdmin))
	if !r.Success {
		t.Fatalf("changeAdmin 失敗: %v", r.Err)
	}
	adm := queryProxy(t, world, rt, creator, ProxyAdminCalldata())
	admHex := toHex32(adm)
	if !strings.HasSuffix(admHex, strings.TrimPrefix(newAdmin, "0x")) {
		t.Fatalf("changeAdmin 後 admin() 不符: %x", adm)
	}
	// 舊 admin 現在無權升級。
	evil := "0x" + strings.Repeat("44", 20)
	r2 := execWithWorld(t, world, rt, creator, ProxyUpgradeToCalldata("0x"+strings.Repeat("66", 20)))
	if r2.Success {
		t.Fatal("舊 admin 在 changeAdmin 後仍可升級，應失敗")
	}
	_ = evil
}
