package vm

import (
	"math/big"
	"strings"
	"testing"
)

// P1-③ 鏈上治理（TACGovernor）單測：propose / vote（一人一票、防重複）/
// proposalInfo 查詢 / execute 多數決執行與防重複執行。

func deployTACGov(t *testing.T) (*WorldState, []byte, string, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	govAddr := "0x" + strings.Repeat("dd", 20)
	in.ctx.Caller = creator
	in.ctx.Address = govAddr
	res := in.Execute(TACGovernorInit(), nil)
	if !res.Success {
		t.Fatalf("TACGovernor 部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	if string(res.ReturnData) != string(TACGovernorRuntime()) {
		t.Fatal("部署回傳 runtime 與 TACGovernorRuntime() 不一致")
	}
	return world, res.ReturnData, creator, govAddr
}

func execGov(world *WorldState, runtime []byte, caller, addr string, calldata []byte) *ExecutionResult {
	ctx := NewContext()
	ctx.Gas = 10_000_000
	ix := NewInterpreter(ctx, world, 0, false)
	ix.ctx.Caller = caller
	ix.ctx.Address = addr
	return ix.Execute(runtime, calldata)
}

// govInfo 解析 proposalInfo 回傳（6 words：creator,support,oppose,executed,target,value）。
type govInfo struct {
	creator  string
	support  int64
	oppose   int64
	executed int64
	target   string
	value    int64
}

func parseGovInfo(rd []byte) *govInfo {
	if len(rd) < 192 {
		return nil
	}
	return &govInfo{
		creator:  "0x" + hexTail(rd[12:32]),
		support:  BytesToInt(rd[32:64]).Int64(),
		oppose:   BytesToInt(rd[64:96]).Int64(),
		executed: BytesToInt(rd[96:128]).Int64(),
		target:   "0x" + hexTail(rd[140:160]),
		value:    BytesToInt(rd[160:192]).Int64(),
	}
}

func hexTail(b []byte) string {
	const hexdig = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdig[c>>4]
		out[i*2+1] = hexdig[c&0xf]
	}
	return string(out)
}

func TestGovDeploy(t *testing.T) {
	_, rt, _, _ := deployTACGov(t)
	if len(rt) == 0 {
		t.Fatal("runtime 為空")
	}
}

func TestGovProposeVote(t *testing.T) {
	world, rt, creator, govAddr := deployTACGov(t)
	alice := "0x" + strings.Repeat("aa", 20)
	bob := "0x" + strings.Repeat("bb", 20)
	carol := "0x" + strings.Repeat("cc", 20)
	target := "0x" + strings.Repeat("99", 20)

	// propose(target, 1000)
	res := execGov(world, rt, creator, govAddr, TACGovProposeCalldata(toGovWord(target), big.NewInt(1000)))
	if !res.Success {
		t.Fatalf("propose 失敗: %v", res.Err)
	}
	if got := BytesToInt(res.ReturnData).Int64(); got != 1 {
		t.Fatalf("propose 回傳 pid=%d, want 1", got)
	}

	// 3 票：alice/bob 贊成、carol 反對
	for _, v := range []struct {
		caller  string
		support int64
	}{{alice, 1}, {bob, 1}, {carol, 0}} {
		res = execGov(world, rt, v.caller, govAddr, TACGovVoteCalldata(big.NewInt(1), big.NewInt(v.support)))
		if !res.Success {
			t.Fatalf("vote(%s,%d) 失敗: %v", v.caller, v.support, res.Err)
		}
	}

	// proposalInfo(1)
	res = execGov(world, rt, creator, govAddr, TACGovInfoCalldata(big.NewInt(1)))
	if !res.Success {
		t.Fatalf("proposalInfo 失敗: %v", res.Err)
	}
	info := parseGovInfo(res.ReturnData)
	if info == nil {
		t.Fatalf("proposalInfo 回傳長度不足: %d", len(res.ReturnData))
	}
	if info.support != 2 || info.oppose != 1 {
		t.Fatalf("support=%d oppose=%d, want 2/1", info.support, info.oppose)
	}
	if info.executed != 0 {
		t.Fatalf("executed=%d, want 0", info.executed)
	}
	if info.target != target {
		t.Fatalf("target=%s, want %s", info.target, target)
	}
	if info.value != 1000 {
		t.Fatalf("value=%d, want 1000", info.value)
	}
	if info.creator != creator {
		t.Fatalf("creator=%s, want %s", info.creator, creator)
	}
}

func TestGovDuplicateVote(t *testing.T) {
	world, rt, creator, govAddr := deployTACGov(t)
	target := "0x" + strings.Repeat("99", 20)

	res := execGov(world, rt, creator, govAddr, TACGovProposeCalldata(toGovWord(target), big.NewInt(1)))
	if !res.Success {
		t.Fatalf("propose 失敗: %v", res.Err)
	}
	if !execGov(world, rt, creator, govAddr, TACGovVoteCalldata(big.NewInt(1), big.NewInt(1))).Success {
		t.Fatal("首次 vote 應成功")
	}
	dup := execGov(world, rt, creator, govAddr, TACGovVoteCalldata(big.NewInt(1), big.NewInt(1)))
	if dup.Success {
		t.Fatal("重複 vote 應失敗（revert）")
	}
	// 計數不變
	res = execGov(world, rt, creator, govAddr, TACGovInfoCalldata(big.NewInt(1)))
	info := parseGovInfo(res.ReturnData)
	if info.support != 1 {
		t.Fatalf("重複投票後 support=%d, want 1", info.support)
	}
}

func TestGovExecute(t *testing.T) {
	world, rt, creator, govAddr := deployTACGov(t)
	alice := "0x" + strings.Repeat("aa", 20)
	bob := "0x" + strings.Repeat("bb", 20)
	target := "0x" + strings.Repeat("99", 20)
	// 給治理合約餘額供 execute 轉帳
	world.SetBalance(govAddr, big.NewInt(100_000))

	res := execGov(world, rt, creator, govAddr, TACGovProposeCalldata(toGovWord(target), big.NewInt(3000)))
	if !res.Success {
		t.Fatalf("propose 失敗: %v", res.Err)
	}
	for _, v := range []struct {
		caller  string
		support int64
	}{{alice, 1}, {bob, 1}} {
		if !execGov(world, rt, v.caller, govAddr, TACGovVoteCalldata(big.NewInt(1), big.NewInt(v.support))).Success {
			t.Fatalf("vote(%s) 失敗", v.caller)
		}
	}
	// execute(1)
	res = execGov(world, rt, alice, govAddr, TACGovExecuteCalldata(big.NewInt(1)))
	if !res.Success {
		t.Fatalf("execute 失敗: %v", res.Err)
	}
	if got := world.GetBalance(target).Int64(); got != 3000 {
		t.Fatalf("target 餘額=%d, want 3000", got)
	}
	if got := world.GetBalance(govAddr).Int64(); got != 100_000-3000 {
		t.Fatalf("gov 餘額=%d, want %d", got, 100_000-3000)
	}
	// executed=1
	res = execGov(world, rt, creator, govAddr, TACGovInfoCalldata(big.NewInt(1)))
	info := parseGovInfo(res.ReturnData)
	if info.executed != 1 {
		t.Fatalf("executed=%d, want 1", info.executed)
	}
	// 重複 execute 應失敗
	if execGov(world, rt, alice, govAddr, TACGovExecuteCalldata(big.NewInt(1))).Success {
		t.Fatal("重複 execute 應失敗")
	}
}

func TestGovExecuteBlocked(t *testing.T) {
	world, rt, creator, govAddr := deployTACGov(t)
	alice := "0x" + strings.Repeat("aa", 20)
	bob := "0x" + strings.Repeat("bb", 20)
	target := "0x" + strings.Repeat("99", 20)
	world.SetBalance(govAddr, big.NewInt(100_000))

	res := execGov(world, rt, creator, govAddr, TACGovProposeCalldata(toGovWord(target), big.NewInt(3000)))
	if !res.Success {
		t.Fatalf("propose 失敗: %v", res.Err)
	}
	// 反對多於贊成：alice 反對、bob 贊成 → 1:1 平手（support 未大於 oppose）
	for _, v := range []struct {
		caller  string
		support int64
	}{{alice, 0}, {bob, 1}} {
		if !execGov(world, rt, v.caller, govAddr, TACGovVoteCalldata(big.NewInt(1), big.NewInt(v.support))).Success {
			t.Fatalf("vote(%s) 失敗", v.caller)
		}
	}
	if execGov(world, rt, alice, govAddr, TACGovExecuteCalldata(big.NewInt(1))).Success {
		t.Fatal("平手時 execute 應失敗")
	}
	// target 未收款、executed=0
	if got := world.GetBalance(target).Int64(); got != 0 {
		t.Fatalf("target 餘額=%d, want 0", got)
	}
	res = execGov(world, rt, creator, govAddr, TACGovInfoCalldata(big.NewInt(1)))
	info := parseGovInfo(res.ReturnData)
	if info.executed != 0 {
		t.Fatalf("executed=%d, want 0", info.executed)
	}
	// 不存在的 pid 執行應失敗
	if execGov(world, rt, alice, govAddr, TACGovExecuteCalldata(big.NewInt(99))).Success {
		t.Fatal("不存在的 pid execute 應失敗")
	}
}

func toGovWord(addr0x string) *big.Int {
	b := hexTail2(strings.TrimPrefix(addr0x, "0x"))
	return BytesToInt(b)
}

func hexTail2(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		out[i] = hexVal(s[i*2])<<4 | hexVal(s[i*2+1])
	}
	return out
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
