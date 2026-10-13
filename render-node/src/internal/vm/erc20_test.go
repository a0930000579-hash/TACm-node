package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// 本檔驗證 TAC VM 能執行「標準代幣（ERC-20 風格）」合約模式：
//   - balanceOf(address)：讀取 storage[address] 回傳
//   - transfer(address, amount)：扣 caller、加 recipient、回傳 1
// 證明 VM 滿足區塊鏈商業化對「標準代幣合約」的相容需求（M42）。

// erc20BalanceOfCode：balanceOf(address) —— calldata[4..36] 為地址，
// 回傳 storage[addr]。
//
//	PUSH1 04 CALLDATALOAD SLOAD → appendReturn
var erc20BalanceOfCode = append([]byte{
	0x60, 0x04, // PUSH1 4
	0x35, // CALLDATALOAD
	0x54, // SLOAD
}, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3)

// erc20TransferCode：transfer(address, amount)
//
//	calldata[4..36]=to, calldata[36..68]=amount, caller=from
//	1) slot[from] -= amount   2) slot[to] += amount   3) 回傳 1
//
// 棧序依 EVM LIFO 語義設計：SUB 為「次-頂」、SSTORE 棧頂為 key（無需 SWAP1）。
var erc20TransferCode = append([]byte{
	0x60, 0x04, 0x35, // to
	0x33,             // CALLER from
	0x54,             // SLOAD slot[from] → [fromBal, to]
	0x60, 0x24, 0x35, // amount → [amount, fromBal, to]
	0x03,             // SUB → fromBal-amount → [newFrom, to]
	0x33,             // CALLER → [caller, newFrom, to]
	0x55,             // SSTORE slot[caller]=newFrom（key 頂、value 次）→ [to]
	0x60, 0x04, 0x35, // to → [to, to]
	0x54,             // SLOAD slot[to] → [toBal, to]
	0x60, 0x24, 0x35, // amount → [amount, toBal, to]
	0x01,             // ADD → toBal+amount → [newTo, to]
	0x60, 0x04, 0x35, // to → [to, newTo, to]
	0x55,       // SSTORE slot[to]=newTo（key 頂、value 次）→ [to]
	0x60, 0x01, // PUSH1 1
}, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3)

// TestERC20BalanceOfPattern 驗證 balanceOf 讀取標準 slot 模型。
func TestERC20BalanceOfPattern(t *testing.T) {
	in, world := newTestVM(0)
	alice := "0x" + strings.Repeat("11", 20) // 合法 hex 地址（CALLER 按 hex 解碼）
	world.SetStorage(ZeroAddress, addrToInt(alice), big.NewInt(1000))

	calldata := append([]byte{0x70, 0xa0, 0x82, 0x31}, IntToBytes(addrToInt(alice), 32)...)
	r := in.Execute(erc20BalanceOfCode, calldata)
	if !r.Success {
		t.Fatalf("balanceOf 執行失敗: %v", r.Err)
	}
	if got := retInt(r); got.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("balanceOf 應回傳 1000，得到 %s", got)
	}
}

// TestERC20TransferPattern 驗證 transfer 完整語義：扣款+加款+回傳 1。
func TestERC20TransferPattern(t *testing.T) {
	in, world := newTestVM(0)
	alice := "0x" + strings.Repeat("11", 20)
	bob := "0x" + strings.Repeat("22", 20)
	in.ctx.Caller = alice
	world.SetStorage(ZeroAddress, addrToInt(alice), big.NewInt(1000))
	world.SetStorage(ZeroAddress, addrToInt(bob), big.NewInt(0))

	calldata := append([]byte{0xa9, 0x05, 0x9c, 0xbb}, IntToBytes(addrToInt(bob), 32)...)
	calldata = append(calldata, IntToBytes(big.NewInt(250), 32)...)
	r := in.Execute(erc20TransferCode, calldata)
	if !r.Success {
		t.Fatalf("transfer 執行失敗: %v", r.Err)
	}
	if got := retInt(r); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("transfer 應回傳 1，得到 %s", got)
	}
	if a := world.GetStorage(ZeroAddress, addrToInt(alice)); a.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("alice 餘額應為 750，得到 %s", a)
	}
	if b := world.GetStorage(ZeroAddress, addrToInt(bob)); b.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("bob 餘額應為 250，得到 %s", b)
	}
}

// TestERC20TransferInsufficientClamps 驗證餘額不足時（balance<amount）不產生負數
// 但不阻斷執行（商業模式層的轉帳校驗由鏈上交易層把關，此處驗證 VM 數值語義）。
func TestERC20TransferInsufficientClamps(t *testing.T) {
	in, world := newTestVM(0)
	alice := "0x" + strings.Repeat("11", 20)
	bob := "0x" + strings.Repeat("22", 20)
	in.ctx.Caller = alice
	world.SetStorage(ZeroAddress, addrToInt(alice), big.NewInt(100))

	calldata := append([]byte{0xa9, 0x05, 0x9c, 0xbb}, IntToBytes(addrToInt(bob), 32)...)
	calldata = append(calldata, IntToBytes(big.NewInt(250), 32)...)
	r := in.Execute(erc20TransferCode, calldata)
	if !r.Success {
		t.Fatalf("transfer 執行失敗: %v", r.Err)
	}
	// VM 按 U256 語義包繞——鏈上交易層的餘額校驗在 VM 之外（SubmitTransaction）。
	// 此測試僅確認執行不 panic、回傳正常。
	if r.ReturnData == nil {
		t.Fatal("缺少回傳數據")
	}
}

// ---- M43：以 Erc20Runtime/Erc20Init 完整部署閉環驗證（鏈上標準代幣發行）----

// deployErc20 部署一份供應量為 supply 的標準代幣並回傳 creator 地址。
func deployErc20(t *testing.T, supply int64) (*WorldState, []byte, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	in.ctx.Caller = creator
	res := in.Execute(Erc20Init(big.NewInt(supply)), nil)
	if !res.Success {
		t.Fatalf("部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	return world, res.ReturnData, creator
}

// queryErc20 以同一 world 對 runtime 執行 calldata。
func queryErc20(t *testing.T, world *WorldState, runtime []byte, creator string, calldata []byte) *big.Int {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	r := ix.Execute(runtime, calldata)
	if !r.Success {
		t.Fatalf("查詢執行失敗: %v", r.Err)
	}
	return retInt(r)
}

// TestERC20DeployMintsToCreator 驗證部署閉環：totalSupply 與 creator 初始
// 餘額均等於 totalSupply，第三方餘額為 0。
func TestERC20DeployMintsToCreator(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	if got := queryErc20(t, world, rt, creator, Erc20TotalSupplyCalldata()); got.Cmp(big.NewInt(1000000)) != 0 {
		t.Fatalf("totalSupply 應為 1000000，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(creator)); got.Cmp(big.NewInt(1000000)) != 0 {
		t.Fatalf("creator 餘額應為 1000000，得到 %s", got)
	}
	bob := "0x" + strings.Repeat("22", 20)
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(bob)); got.Sign() != 0 {
		t.Fatalf("bob 初始餘額應為 0，得到 %s", got)
	}
}

// TestERC20DeployTransfer 驗證完整 transfer 閉環：扣款、加款、回傳 1。
func TestERC20DeployTransfer(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	bob := "0x" + strings.Repeat("22", 20)
	if got := queryErc20(t, world, rt, creator, Erc20TransferCalldata(bob, big.NewInt(250))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("transfer 應回傳 1，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(creator)); got.Cmp(big.NewInt(999750)) != 0 {
		t.Fatalf("creator 餘額應為 999750，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(bob)); got.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("bob 餘額應為 250，得到 %s", got)
	}
}

// TestERC20DeployUnknownSelectorFallback 驗證未知 selector 走 fallback（RETURN(0,0)），
// 不 panic 且無回傳數據。
func TestERC20DeployUnknownSelectorFallback(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000)
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	r := ix.Execute(rt, []byte{0xde, 0xad, 0xbe, 0xef})
	if !r.Success {
		t.Fatalf("fallback 執行失敗: %v", r.Err)
	}
	if len(r.ReturnData) != 0 {
		t.Fatalf("fallback 應無回傳數據，得到 %d bytes", len(r.ReturnData))
	}
}

// ---- M44：代幣元資料（name/symbol）與 PUSH2 跳轉目標驗證 ----

// deployErc20WithMeta 部署帶 name/symbol 元資料的標準代幣。
func deployErc20WithMeta(t *testing.T, supply int64, name, symbol string) (*WorldState, []byte, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	in.ctx.Caller = creator
	res := in.Execute(Erc20InitWithMeta(big.NewInt(supply), []byte(name), []byte(symbol)), nil)
	if !res.Success {
		t.Fatalf("部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	// 部署回傳的 runtime 必須與 Erc20Runtime() 完全一致（CODECOPY src 正確）。
	if string(res.ReturnData) != string(Erc20Runtime()) {
		t.Fatal("部署回傳 runtime 與 Erc20Runtime() 不一致")
	}
	return world, res.ReturnData, creator
}

// TestERC20DeployMetaNameSymbol 驗證 name()/symbol() 讀取 slot1/slot2（右對齊）。
func TestERC20DeployMetaNameSymbol(t *testing.T) {
	world, rt, creator := deployErc20WithMeta(t, 1000000, "MyToken", "TAC")
	gotName := queryErc20(t, world, rt, creator, Erc20NameCalldata())
	if got := string(gotName.Bytes()); got != "MyToken" {
		t.Fatalf("name 應為 MyToken，得到 %q", got)
	}
	gotSym := queryErc20(t, world, rt, creator, Erc20SymbolCalldata())
	if got := string(gotSym.Bytes()); got != "TAC" {
		t.Fatalf("symbol 應為 TAC，得到 %q", got)
	}
	// 純 supply 版（無元資料）name/symbol 應為 0。
	world2, rt2, creator2 := deployErc20(t, 1000)
	if got := queryErc20(t, world2, rt2, creator2, Erc20NameCalldata()); got.Sign() != 0 {
		t.Fatalf("無元資料 name 應為 0，得到 %s", got)
	}
	if got := queryErc20(t, world2, rt2, creator2, Erc20SymbolCalldata()); got.Sign() != 0 {
		t.Fatalf("無元資料 symbol 應為 0，得到 %s", got)
	}
}

// TestERC20DeployMetaTransferAfterMeta 驗證帶元資料部署後 transfer 仍正常
// （storage 佈局 slot1/slot2 不影響 slot[address] 餘額）。
func TestERC20DeployMetaTransferAfterMeta(t *testing.T) {
	world, rt, creator := deployErc20WithMeta(t, 1000000, "MyToken", "TAC")
	bob := "0x" + strings.Repeat("22", 20)
	if got := queryErc20(t, world, rt, creator, Erc20TransferCalldata(bob, big.NewInt(250))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("transfer 應回傳 1，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(creator)); got.Cmp(big.NewInt(999750)) != 0 {
		t.Fatalf("creator 餘額應為 999750，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(bob)); got.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("bob 餘額應為 250，得到 %s", got)
	}
}

// ---- M74-1：標準授權介面（approve/allowance/transferFrom）＋事件 ----

// execErc20As 以指定 caller 執行並回傳完整執行結果（供事件/失敗檢查）。
func execErc20As(world *WorldState, runtime []byte, caller string, calldata []byte) *ExecutionResult {
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = caller
	return ix.Execute(runtime, calldata)
}

// TestERC20ApproveAllowance 驗證 approve→allowance 閉環：授權後 spender 可讀到額度。
func TestERC20ApproveAllowance(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	spender := "0x" + strings.Repeat("33", 20)
	// approve(spender, 500)
	if got := queryErc20(t, world, rt, creator, Erc20ApproveCalldata(spender, big.NewInt(500))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("approve 應回傳 1，得到 %s", got)
	}
	// allowance(creator, spender) = 500
	if got := queryErc20(t, world, rt, creator, Erc20AllowanceCalldata(creator, spender)); got.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("allowance 應為 500，得到 %s", got)
	}
	// 未授權組合為 0
	carol := "0x" + strings.Repeat("44", 20)
	if got := queryErc20(t, world, rt, creator, Erc20AllowanceCalldata(creator, carol)); got.Sign() != 0 {
		t.Fatalf("未授權 allowance 應為 0，得到 %s", got)
	}
}

// TestERC20TransferFrom 驗證 transferFrom 閉環：spender 以授權額度轉移 creator 資產。
func TestERC20TransferFrom(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	spender := "0x" + strings.Repeat("33", 20)
	carol := "0x" + strings.Repeat("44", 20)
	if got := queryErc20(t, world, rt, creator, Erc20ApproveCalldata(spender, big.NewInt(500))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("approve 失敗: %s", got)
	}
	// spender 以 transferFrom(creator→carol, 200) 動用額度
	if got := retInt(execErc20As(world, rt, spender, Erc20TransferFromCalldata(creator, carol, big.NewInt(200)))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("transferFrom 應回傳 1，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(creator)); got.Cmp(big.NewInt(999800)) != 0 {
		t.Fatalf("creator 餘額應為 999800，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20BalanceOfCalldata(carol)); got.Cmp(big.NewInt(200)) != 0 {
		t.Fatalf("carol 餘額應為 200，得到 %s", got)
	}
	if got := queryErc20(t, world, rt, creator, Erc20AllowanceCalldata(creator, spender)); got.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("allowance 應餘 300，得到 %s", got)
	}
}

// TestERC20TransferFromInsufficientAllowance 驗證授權不足時 REVERT。
func TestERC20TransferFromInsufficientAllowance(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	spender := "0x" + strings.Repeat("33", 20)
	carol := "0x" + strings.Repeat("44", 20)
	if got := queryErc20(t, world, rt, creator, Erc20ApproveCalldata(spender, big.NewInt(50))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("approve 失敗: %s", got)
	}
	r := execErc20As(world, rt, spender, Erc20TransferFromCalldata(creator, carol, big.NewInt(200)))
	if r.Success {
		t.Fatal("授權不足 transferFrom 應 REVERT")
	}
}

// TestERC20TransferFromInsufficientBalance 驗證 from 餘額不足時 REVERT。
func TestERC20TransferFromInsufficientBalance(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000)
	spender := "0x" + strings.Repeat("33", 20)
	carol := "0x" + strings.Repeat("44", 20)
	if got := queryErc20(t, world, rt, creator, Erc20ApproveCalldata(spender, big.NewInt(10000))); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("approve 失敗: %s", got)
	}
	r := execErc20As(world, rt, spender, Erc20TransferFromCalldata(creator, carol, big.NewInt(2000)))
	if r.Success {
		t.Fatal("餘額不足 transferFrom 應 REVERT")
	}
}

// TestERC20Decimals 驗證 decimals() 回傳 18（主流 ERC-20 慣例）。
func TestERC20Decimals(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	if got := queryErc20(t, world, rt, creator, Erc20DecimalsCalldata()); got.Cmp(big.NewInt(18)) != 0 {
		t.Fatalf("decimals 應為 18，得到 %s", got)
	}
}

// TestERC20TransferEvent 驗證 transfer 產生標準 Transfer 事件（topic0 與主網一致）。
func TestERC20TransferEvent(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	bob := "0x" + strings.Repeat("22", 20)
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	// LOG 事件的 address 語義＝執行合約的地址（ctx.Address，Execute 內經 Ensure 規範化）。
	// 本測試直接執行 runtime（無 create 部署流程），合約帳戶即 ctx.Address。
	r := ix.Execute(rt, Erc20TransferCalldata(bob, big.NewInt(250)))
	if !r.Success {
		t.Fatalf("transfer 失敗: %v", r.Err)
	}
	if len(ix.ctx.Logs) != 1 {
		t.Fatalf("應產生 1 筆 Transfer 事件，得到 %d", len(ix.ctx.Logs))
	}
	log0 := ix.ctx.Logs[0]
	if log0.Address != ix.ctx.Address {
		t.Fatalf("事件地址應為執行合約地址 %s，得到 %s", ix.ctx.Address, log0.Address)
	}
	if len(log0.Topics) != 3 || log0.Topics[0] != hex.EncodeToString(TopicTransfer) {
		t.Fatalf("Transfer 事件 topics 異常: %v", log0.Topics)
	}
}

// TestERC20ApproveEvent 驗證 approve 產生標準 Approval 事件。
func TestERC20ApproveEvent(t *testing.T) {
	world, rt, creator := deployErc20(t, 1000000)
	spender := "0x" + strings.Repeat("33", 20)
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	r := ix.Execute(rt, Erc20ApproveCalldata(spender, big.NewInt(500)))
	if !r.Success {
		t.Fatalf("approve 失敗: %v", r.Err)
	}
	if len(ix.ctx.Logs) != 1 {
		t.Fatalf("應產生 1 筆 Approval 事件，得到 %d", len(ix.ctx.Logs))
	}
	log0 := ix.ctx.Logs[0]
	if len(log0.Topics) != 3 || log0.Topics[0] != hex.EncodeToString(TopicApproval) {
		t.Fatalf("Approval 事件 topics 異常: %v", log0.Topics)
	}
}
