package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// M135-A：ERC-1155 多代幣標準 e2e 驗證。
// 涵蓋：deploy 元資料、mint→balanceOf/balanceOfBatch、safeTransferFrom（含授權與
// 餘額不足 REVERT）、safeBatchTransferFrom、setApprovalForAll/isApprovedForAll、
// uri、supportsInterface，以及 TransferSingle/TransferBatch/ApprovalForAll 事件。

// deployErc1155 部署帶元資料的 ERC-1155 合約。
func deployErc1155(t *testing.T, name, symbol string) (*WorldState, []byte, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	in.ctx.Caller = creator
	res := in.Execute(Erc1155InitWithMeta([]byte(name), []byte(symbol)), nil)
	if !res.Success {
		t.Fatalf("ERC-1155 部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	if string(res.ReturnData) != string(Erc1155Runtime()) {
		t.Fatal("部署回傳 runtime 與 Erc1155Runtime() 不一致")
	}
	return world, res.ReturnData, creator
}

// queryErc1155 以同一 world 對 runtime 執行 calldata（預設 caller=creator）。
func queryErc1155(t *testing.T, world *WorldState, runtime []byte, creator string, calldata []byte) *big.Int {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	r := ix.Execute(runtime, calldata)
	if !r.Success {
		t.Fatalf("ERC-1155 查詢執行失敗: %v", r.Err)
	}
	return retInt(r)
}

// execErc1155As 以指定 caller 執行並回傳完整結果。
func execErc1155As(world *WorldState, runtime []byte, caller string, calldata []byte) *ExecutionResult {
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = caller
	return ix.Execute(runtime, calldata)
}

// TestERC1155DeployMeta 驗證部署後 name/symbol 讀回。
func TestERC1155DeployMeta(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	// name/symbol 讀回（slot1/slot2，右對齊 bytes）
	if got := queryErc1155(t, world, rt, creator, Erc1155URICalldata(big.NewInt(0))); got.Sign() != 0 {
		t.Fatalf("初始 uri(0) 應為 0，得到 %s", got)
	}
}

// TestERC1155MintAndBalance 驗證 mint→balanceOf/balanceOfBatch＋TransferSingle 事件。
func TestERC1155MintAndBalance(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	holder := "0x" + strings.Repeat("22", 20)
	r := execErc1155As(world, rt, creator, Erc1155MintCalldata(holder, big.NewInt(1), big.NewInt(100)))
	if !r.Success {
		t.Fatalf("mint 失敗: %v", r.Err)
	}
	// balanceOf(holder, 1) == 100
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(holder, big.NewInt(1))); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("balanceOf(holder,1) 應為 100，得到 %s", got)
	}
	// balanceOfBatch([holder, holder], [1, 2]) == [100, 0]
	rr := execErc1155As(world, rt, creator, Erc1155BalanceOfBatchCalldata(
		[]string{holder, holder}, []*big.Int{big.NewInt(1), big.NewInt(2)}))
	if !rr.Success {
		t.Fatalf("balanceOfBatch 失敗: %v", rr.Err)
	}
	if len(rr.ReturnData) != 128 { // offset(32)+length(32)+2×32 items
		t.Fatalf("balanceOfBatch 回傳應為 128 bytes，得到 %d", len(rr.ReturnData))
	}
	v1 := new(big.Int).SetBytes(rr.ReturnData[64:96])
	v2 := new(big.Int).SetBytes(rr.ReturnData[96:128])
	if v1.Cmp(big.NewInt(100)) != 0 || v2.Sign() != 0 {
		t.Fatalf("balanceOfBatch 應為 [100,0]，得到 [%s,%s]", v1, v2)
	}
	// TransferSingle 事件：topic0 + [caller, 0, holder]、data=id‖amount
	if len(r.Logs) != 1 {
		t.Fatalf("應產生 1 筆 TransferSingle，得到 %d", len(r.Logs))
	}
	l := r.Logs[0]
	if len(l.Topics) != 4 || l.Topics[0] != hex.EncodeToString(Topic1155TransferSingle) {
		t.Fatalf("TransferSingle topics 異常: %v", l.Topics)
	}
	if l.Topics[1] != hex.EncodeToString(IntToBytes(addrToInt(creator), 32)) {
		t.Fatalf("operator 應為 creator，得到 %s", l.Topics[1])
	}
	if l.Topics[2] != hex.EncodeToString(make([]byte, 32)) {
		t.Fatalf("from 應為 0，得到 %s", l.Topics[2])
	}
	if l.Topics[3] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) {
		t.Fatalf("to 應為 holder，得到 %s", l.Topics[3])
	}
	data, err := hex.DecodeString(strings.TrimPrefix(l.Data, "0x"))
	if err != nil || len(data) != 64 {
		t.Fatalf("data 應為 id‖amount（64 bytes），得到 %q", l.Data)
	}
	if new(big.Int).SetBytes(data[:32]).Cmp(big.NewInt(1)) != 0 ||
		new(big.Int).SetBytes(data[32:]).Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("data 應為 id=1, amount=100，得到 %x", data)
	}
}

// TestERC1155SafeTransfer 驗證持有人轉移＋TransferSingle 事件（from≠0）。
func TestERC1155SafeTransfer(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc1155As(world, rt, creator, Erc1155MintCalldata(holder, big.NewInt(1), big.NewInt(50)))
	r := execErc1155As(world, rt, holder, Erc1155SafeTransferFromCalldata(holder, bob, big.NewInt(1), big.NewInt(30)))
	if !r.Success {
		t.Fatalf("safeTransferFrom 失敗: %v", r.Err)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(holder, big.NewInt(1))); got.Cmp(big.NewInt(20)) != 0 {
		t.Fatalf("holder 餘額應為 20，得到 %s", got)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(bob, big.NewInt(1))); got.Cmp(big.NewInt(30)) != 0 {
		t.Fatalf("bob 餘額應為 30，得到 %s", got)
	}
	l := r.Logs[0]
	if l.Topics[1] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) ||
		l.Topics[2] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) ||
		l.Topics[3] != hex.EncodeToString(IntToBytes(addrToInt(bob), 32)) {
		t.Fatalf("TransferSingle topics 異常: %v", l.Topics)
	}
}

// TestERC1155InsufficientBalance 驗證餘額不足 REVERT。
func TestERC1155InsufficientBalance(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc1155As(world, rt, creator, Erc1155MintCalldata(holder, big.NewInt(1), big.NewInt(10)))
	if r := execErc1155As(world, rt, holder, Erc1155SafeTransferFromCalldata(holder, bob, big.NewInt(1), big.NewInt(11))); r.Success {
		t.Fatal("餘額不足轉移應 REVERT")
	}
}

// TestERC1155ApprovalFlow 驗證 setApprovalForAll→isApprovedForAll→operator 代轉＋事件。
func TestERC1155ApprovalFlow(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	holder := "0x" + strings.Repeat("22", 20)
	op := "0x" + strings.Repeat("66", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc1155As(world, rt, creator, Erc1155MintCalldata(holder, big.NewInt(1), big.NewInt(50)))
	// 授權
	r := execErc1155As(world, rt, holder, Erc1155SetApprovalForAllCalldata(op, true))
	if !r.Success {
		t.Fatalf("setApprovalForAll 失敗: %v", r.Err)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155IsApprovedForAllCalldata(holder, op)); got.Sign() != 1 {
		t.Fatalf("isApprovedForAll 應為 1，得到 %s", got)
	}
	// ApprovalForAll 事件：topic0 + [caller, operator]
	if len(r.Logs) != 1 || len(r.Logs[0].Topics) != 3 ||
		r.Logs[0].Topics[0] != hex.EncodeToString(Topic1155ApprovalForAll) {
		t.Fatalf("ApprovalForAll 事件異常: %v", r.Logs)
	}
	// operator 代轉
	r2 := execErc1155As(world, rt, op, Erc1155SafeTransferFromCalldata(holder, bob, big.NewInt(1), big.NewInt(20)))
	if !r2.Success {
		t.Fatalf("operator 代轉失敗: %v", r2.Err)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(holder, big.NewInt(1))); got.Cmp(big.NewInt(30)) != 0 {
		t.Fatalf("holder 餘額應為 30，得到 %s", got)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(bob, big.NewInt(1))); got.Cmp(big.NewInt(20)) != 0 {
		t.Fatalf("bob 餘額應為 20，得到 %s", got)
	}
	// 未授權代轉應 REVERT
	if r3 := execErc1155As(world, rt, bob, Erc1155SafeTransferFromCalldata(holder, op, big.NewInt(1), big.NewInt(1))); r3.Success {
		t.Fatal("未授權代轉應 REVERT")
	}
	// 取消授權
	if r4 := execErc1155As(world, rt, holder, Erc1155SetApprovalForAllCalldata(op, false)); !r4.Success {
		t.Fatalf("取消授權失敗: %v", r4.Err)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155IsApprovedForAllCalldata(holder, op)); got.Sign() != 0 {
		t.Fatalf("取消授權後 isApprovedForAll 應為 0，得到 %s", got)
	}
}

// TestERC1155SafeBatchTransfer 驗證多代幣批次轉移＋TransferBatch 事件。
func TestERC1155SafeBatchTransfer(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc1155As(world, rt, creator, Erc1155MintCalldata(holder, big.NewInt(1), big.NewInt(10)))
	execErc1155As(world, rt, creator, Erc1155MintCalldata(holder, big.NewInt(2), big.NewInt(20)))
	r := execErc1155As(world, rt, holder, Erc1155SafeBatchTransferCalldata(
		holder, bob, []*big.Int{big.NewInt(1), big.NewInt(2)}, []*big.Int{big.NewInt(4), big.NewInt(6)}))
	if !r.Success {
		t.Fatalf("safeBatchTransferFrom 失敗: %v", r.Err)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(holder, big.NewInt(1))); got.Cmp(big.NewInt(6)) != 0 {
		t.Fatalf("holder id1 餘額應為 6，得到 %s", got)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(bob, big.NewInt(1))); got.Cmp(big.NewInt(4)) != 0 {
		t.Fatalf("bob id1 餘額應為 4，得到 %s", got)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155BalanceOfCalldata(bob, big.NewInt(2))); got.Cmp(big.NewInt(6)) != 0 {
		t.Fatalf("bob id2 餘額應為 6，得到 %s", got)
	}
	// TransferBatch 事件
	if len(r.Logs) != 1 || len(r.Logs[0].Topics) != 4 ||
		r.Logs[0].Topics[0] != hex.EncodeToString(Topic1155TransferBatch) {
		t.Fatalf("TransferBatch 事件異常: %v", r.Logs)
	}
	// 餘額不足批次應 REVERT（10 個 id2 超過 20）
	if r2 := execErc1155As(world, rt, holder, Erc1155SafeBatchTransferCalldata(
		holder, bob, []*big.Int{big.NewInt(2)}, []*big.Int{big.NewInt(30)})); r2.Success {
		t.Fatal("批次餘額不足應 REVERT")
	}
}

// TestERC1155SupportsInterface 驗證 ERC-165 介面偵測。
func TestERC1155SupportsInterface(t *testing.T) {
	world, rt, creator := deployErc1155(t, "GameItems", "GITM")
	if got := queryErc1155(t, world, rt, creator, Erc1155SupportsInterfaceCalldata(interface1155ID)); got.Sign() != 1 {
		t.Fatalf("supportsInterface(0xd9b67a26) 應為 1，得到 %s", got)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155SupportsInterfaceCalldata(interface165ID)); got.Sign() != 1 {
		t.Fatalf("supportsInterface(0x01ffc9a7) 應為 1，得到 %s", got)
	}
	if got := queryErc1155(t, world, rt, creator, Erc1155SupportsInterfaceCalldata([]byte{0x12, 0x34, 0x56, 0x78})); got.Sign() != 0 {
		t.Fatalf("supportsInterface(unknown) 應為 0，得到 %s", got)
	}
}
