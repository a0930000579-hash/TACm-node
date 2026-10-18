package vm

import (
	"math/big"
	"strings"
	"testing"
)

// TAC-ENS 鏈上命名服務（P1-①）單測：
// register/renew/setAddr/transfer 的權限與狀態機、過期重註冊、反向解析、ERC-165。

const nsTS = 1700000000   // 固定區塊時間（unix 秒）
const nsYear = 31536000   // 一年秒數

func deployTACNames(t *testing.T) (*WorldState, []byte, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	in.ctx.Caller = creator
	in.ctx.BlockTimestamp = nsTS
	res := in.Execute(TACNamesInit(), nil)
	if !res.Success {
		t.Fatalf("TACNames 部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	if string(res.ReturnData) != string(TACNamesRuntime()) {
		t.Fatal("部署回傳 runtime 與 TACNamesRuntime() 不一致")
	}
	return world, res.ReturnData, creator
}

func execNS(world *WorldState, runtime []byte, caller string, ts int64, calldata []byte) *ExecutionResult {
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = caller
	ix.ctx.BlockTimestamp = ts
	return ix.Execute(runtime, calldata)
}

func queryNS(t *testing.T, world *WorldState, runtime []byte, caller string, ts int64, calldata []byte) *big.Int {
	t.Helper()
	r := execNS(world, runtime, caller, ts, calldata)
	if !r.Success {
		t.Fatalf("執行失敗: %v", r.Err)
	}
	return retInt(r)
}

func TestTACNamesRegisterResolve(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("titan"))
	user := "0x" + strings.Repeat("22", 20)

	if got := queryNS(t, world, rt, user, nsTS, TACNamesNameExistsCalldata(labelHash)); got.Sign() != 0 {
		t.Fatalf("初始 nameExists 應為 0，得到 %v", got)
	}

	r := execNS(world, rt, user, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear)))
	if !r.Success || r.Reverted {
		t.Fatalf("register 失敗: %v reverted=%v", r.Err, r.Reverted)
	}

	if got := queryNS(t, world, rt, user, nsTS, TACNamesOwnerCalldata(labelHash)); got.String() != addrToInt(user).String() {
		t.Fatalf("owner 應為 %s，得到 %v", user, got)
	}
	want := big.NewInt(nsTS + nsYear)
	if got := queryNS(t, world, rt, user, nsTS, TACNamesExpiryCalldata(labelHash)); got.Cmp(want) != 0 {
		t.Fatalf("expiry 應為 %v，得到 %v", want, got)
	}
	if got := queryNS(t, world, rt, user, nsTS, TACNamesAddrCalldata(labelHash)); got.Sign() != 0 {
		t.Fatalf("初始 addr 應為 0，得到 %v", got)
	}

	r = execNS(world, rt, user, nsTS, TACNamesSetAddrCalldata(labelHash, user))
	if !r.Success || r.Reverted {
		t.Fatalf("setAddr 失敗: %v", r.Err)
	}
	if got := queryNS(t, world, rt, user, nsTS, TACNamesAddrCalldata(labelHash)); got.String() != addrToInt(user).String() {
		t.Fatalf("addr 應為 %s，得到 %v", user, got)
	}
	if got := queryNS(t, world, rt, user, nsTS, TACNamesResolveCalldata(labelHash)); got.String() != addrToInt(user).String() {
		t.Fatalf("resolve 應為 %s，得到 %v", user, got)
	}
	if got := queryNS(t, world, rt, user, nsTS, TACNamesReverseCalldata(user)); got.String() != BytesToInt(labelHash).String() {
		t.Fatalf("reverse 應為 %x，得到 %v", labelHash, got)
	}
	if got := queryNS(t, world, rt, user, nsTS, TACNamesNameExistsCalldata(labelHash)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("nameExists 應為 1，得到 %v", got)
	}
}

func TestTACNamesDuplicateRegisterReverts(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("dup"))
	u1 := "0x" + strings.Repeat("33", 20)
	u2 := "0x" + strings.Repeat("44", 20)
	if r := execNS(world, rt, u1, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear))); !r.Success || r.Reverted {
		t.Fatalf("首次 register 失敗: %v", r.Err)
	}
	r := execNS(world, rt, u2, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear)))
	if r.Success && !r.Reverted {
		t.Fatalf("使用中的名稱應被拒，但成功")
	}
}

func TestTACNamesTransferOwnerOnly(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("move"))
	owner := "0x" + strings.Repeat("55", 20)
	other := "0x" + strings.Repeat("66", 20)
	next := "0x" + strings.Repeat("77", 20)
	execNS(world, rt, owner, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear)))

	if r := execNS(world, rt, other, nsTS, TACNamesTransferCalldata(labelHash, next)); r.Success && !r.Reverted {
		t.Fatal("非 owner transfer 應被拒")
	}
	if r := execNS(world, rt, owner, nsTS, TACNamesTransferCalldata(labelHash, next)); !r.Success || r.Reverted {
		t.Fatalf("owner transfer 失敗: %v", r.Err)
	}
	if got := queryNS(t, world, rt, next, nsTS, TACNamesOwnerCalldata(labelHash)); got.String() != addrToInt(next).String() {
		t.Fatalf("transfer 後 owner 應為 %s，得到 %v", next, got)
	}
}

func TestTACNamesSetAddrOwnerOnly(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("addr"))
	owner := "0x" + strings.Repeat("88", 20)
	other := "0x" + strings.Repeat("99", 20)
	execNS(world, rt, owner, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear)))

	if r := execNS(world, rt, other, nsTS, TACNamesSetAddrCalldata(labelHash, other)); r.Success && !r.Reverted {
		t.Fatal("非 owner setAddr 應被拒")
	}
	if r := execNS(world, rt, owner, nsTS, TACNamesSetAddrCalldata(labelHash, other)); !r.Success || r.Reverted {
		t.Fatalf("owner setAddr 失敗: %v", r.Err)
	}
}

func TestTACNamesRenew(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("renew"))
	owner := "0x" + strings.Repeat("aa", 20)
	execNS(world, rt, owner, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear)))

	want := big.NewInt(nsTS + nsYear + nsYear)
	if r := execNS(world, rt, owner, nsTS, TACNamesRenewCalldata(labelHash, big.NewInt(nsYear))); !r.Success || r.Reverted {
		t.Fatalf("renew 失敗: %v", r.Err)
	}
	if got := queryNS(t, world, rt, owner, nsTS, TACNamesExpiryCalldata(labelHash)); got.Cmp(want) != 0 {
		t.Fatalf("renew 後 expiry 應為 %v，得到 %v", want, got)
	}
}

func TestTACNamesExpiredReregister(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("exp"))
	u1 := "0x" + strings.Repeat("bb", 20)
	u2 := "0x" + strings.Repeat("cc", 20)
	execNS(world, rt, u1, nsTS, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear)))

	tsLate := int64(nsTS + nsYear + 10)
	if got := queryNS(t, world, rt, u2, tsLate, TACNamesNameExistsCalldata(labelHash)); got.Sign() != 0 {
		t.Fatalf("過期後 nameExists 應為 0，得到 %v", got)
	}
	if r := execNS(world, rt, u2, tsLate, TACNamesRegisterCalldata(labelHash, big.NewInt(nsYear*2))); !r.Success || r.Reverted {
		t.Fatalf("過期重註冊失敗: %v", r.Err)
	}
	if got := queryNS(t, world, rt, u2, tsLate, TACNamesOwnerCalldata(labelHash)); got.String() != addrToInt(u2).String() {
		t.Fatalf("重註冊後 owner 應為 %s，得到 %v", u2, got)
	}
}

func TestTACNamesRegisterTo(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	labelHash := Keccak256([]byte("agent"))
	proxy := "0x" + strings.Repeat("21", 20) // 節點/服務代註冊者
	owner := "0x" + strings.Repeat("31", 20) // 實際名稱 owner

	r := execNS(world, rt, proxy, nsTS, TACNamesRegisterToCalldata(labelHash, owner, big.NewInt(nsYear)))
	if !r.Success || r.Reverted {
		t.Fatalf("registerTo 失敗: %v reverted=%v", r.Err, r.Reverted)
	}
	if got := queryNS(t, world, rt, owner, nsTS, TACNamesOwnerCalldata(labelHash)); got.String() != addrToInt(owner).String() {
		t.Fatalf("registerTo owner 應為 %s，得到 %v", owner, got)
	}
	if got := queryNS(t, world, rt, owner, nsTS, TACNamesReverseCalldata(owner)); got.String() != BytesToInt(labelHash).String() {
		t.Fatalf("registerTo reverse 應為 %x，得到 %v", labelHash, got)
	}
	if r := execNS(world, rt, proxy, nsTS, TACNamesTransferCalldata(labelHash, proxy)); r.Success && !r.Reverted {
		t.Fatal("非 owner（代理者）transfer 應被拒")
	}
}

func TestTACNamesSupportsInterface(t *testing.T) {
	world, rt, _ := deployTACNames(t)
	caller := "0x" + strings.Repeat("dd", 20)
	if got := queryNS(t, world, rt, caller, nsTS, TACNamesSupportsInterfaceCalldata(Interface165)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("supportsInterface(165) 應為 1，得到 %v", got)
	}
	if got := queryNS(t, world, rt, caller, nsTS, TACNamesSupportsInterfaceCalldata([]byte{0x12, 0x34, 0x56, 0x78})); got.Sign() != 0 {
		t.Fatalf("supportsInterface(未知) 應為 0，得到 %v", got)
	}
}
