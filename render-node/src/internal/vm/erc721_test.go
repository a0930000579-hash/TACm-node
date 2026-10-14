package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// 本檔驗證 TAC VM 能執行「標準 NFT（ERC-721 風格）」合約模式（M74-2）：
//   - mint(address,uint256)：唯一性檢查、ownerOf/balance/totalSupply 更新、Transfer(0,to,id) 事件
//   - transferFrom(from,to,uint256)：僅 owner 本人可轉、ownerOf 更新、雙邊 balance、Transfer 事件
//   - balanceOf/ownerOf/tokenURI/totalSupply/name/symbol 查詢介面

// deployErc721 部署帶元資料（name/symbol）的 ERC-721 合約。
func deployErc721(t *testing.T, name, symbol string) (*WorldState, []byte, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	in.ctx.Caller = creator
	res := in.Execute(Erc721InitWithMeta([]byte(name), []byte(symbol)), nil)
	if !res.Success {
		t.Fatalf("NFT 部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	if string(res.ReturnData) != string(Erc721Runtime()) {
		t.Fatal("部署回傳 runtime 與 Erc721Runtime() 不一致")
	}
	return world, res.ReturnData, creator
}

// queryErc721 以同一 world 對 runtime 執行 calldata（預設 caller=creator）。
func queryErc721(t *testing.T, world *WorldState, runtime []byte, creator string, calldata []byte) *big.Int {
	t.Helper()
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = creator
	r := ix.Execute(runtime, calldata)
	if !r.Success {
		t.Fatalf("NFT 查詢執行失敗: %v", r.Err)
	}
	return retInt(r)
}

// execErc721As 以指定 caller 執行並回傳完整執行結果。
func execErc721As(world *WorldState, runtime []byte, caller string, calldata []byte) *ExecutionResult {
	ix := NewInterpreter(NewContext(), world, 0, false)
	ix.ctx.Caller = caller
	return ix.Execute(runtime, calldata)
}

// TestERC721DeployMeta 驗證 NFT 部署後 name()/symbol() 讀回。
func TestERC721DeployMeta(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	if got := queryErc721(t, world, rt, creator, Erc721NameCalldata()); string(got.Bytes()) != "MyNFT" {
		t.Fatalf("name 應為 MyNFT，得到 %q", got.Bytes())
	}
	if got := queryErc721(t, world, rt, creator, Erc721SymbolCalldata()); string(got.Bytes()) != "MNFT" {
		t.Fatalf("symbol 應為 MNFT，得到 %q", got.Bytes())
	}
	if got := queryErc721(t, world, rt, creator, Erc721TotalSupplyCalldata()); got.Sign() != 0 {
		t.Fatalf("初始 totalSupply 應為 0，得到 %s", got)
	}
}

// TestERC721Mint 驗證 mint 閉環：balanceOf/ownerOf/totalSupply 更新＋標準 Transfer 事件。
func TestERC721Mint(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	r := execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(7)))
	if !r.Success {
		t.Fatalf("mint 失敗: %v", r.Err)
	}
	if got := queryErc721(t, world, rt, creator, Erc721BalanceOfCalldata(holder)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("holder balance 應為 1，得到 %s", got)
	}
	if got := queryErc721(t, world, rt, creator, Erc721OwnerOfCalldata(big.NewInt(7))); got.Cmp(addrToInt(holder)) != 0 {
		t.Fatalf("ownerOf(7) 應為 holder，得到 %s", got)
	}
	if got := queryErc721(t, world, rt, creator, Erc721TotalSupplyCalldata()); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("totalSupply 應為 1，得到 %s", got)
	}
	// Transfer(0, holder, 7) 事件
	if len(r.Logs) != 1 {
		t.Fatalf("應產生 1 筆 Transfer 事件，得到 %d", len(r.Logs))
	}
	log0 := r.Logs[0]
	if log0.Address != ZeroAddress {
		t.Fatalf("事件地址應為執行合約地址 %s，得到 %s", ZeroAddress, log0.Address)
	}
	if len(log0.Topics) != 3 || log0.Topics[0] != hex.EncodeToString(Topic721Transfer) {
		t.Fatalf("Transfer 事件 topics 異常: %v", log0.Topics)
	}
	if log0.Topics[1] != hex.EncodeToString(make([]byte, 32)) {
		t.Fatalf("mint from 應為 0（ZeroAddress），得到 %s", log0.Topics[1])
	}
	if log0.Topics[2] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) {
		t.Fatalf("mint to 應為 holder，得到 %s", log0.Topics[2])
	}
}

// TestERC721MintDuplicate 驗證同一 tokenId 不可重複 mint（REVERT）。
func TestERC721MintDuplicate(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	if r := execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(7))); !r.Success {
		t.Fatalf("首次 mint 應成功: %v", r.Err)
	}
	if r := execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(7))); r.Success {
		t.Fatal("重複 mint 同一 tokenId 應 REVERT")
	}
}

// TestERC721TransferFrom 驗證 owner 本人轉移：ownerOf 更新、雙邊 balance、Transfer 事件。
func TestERC721TransferFrom(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(9)))
	r := execErc721As(world, rt, holder, Erc721TransferFromCalldata(holder, bob, big.NewInt(9)))
	if !r.Success {
		t.Fatalf("owner transferFrom 失敗: %v", r.Err)
	}
	if got := queryErc721(t, world, rt, creator, Erc721OwnerOfCalldata(big.NewInt(9))); got.Cmp(addrToInt(bob)) != 0 {
		t.Fatalf("ownerOf(9) 應為 bob，得到 %s", got)
	}
	if got := queryErc721(t, world, rt, creator, Erc721BalanceOfCalldata(holder)); got.Sign() != 0 {
		t.Fatalf("holder balance 應為 0，得到 %s", got)
	}
	if got := queryErc721(t, world, rt, creator, Erc721BalanceOfCalldata(bob)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("bob balance 應為 1，得到 %s", got)
	}
	if len(r.Logs) != 1 || len(r.Logs[0].Topics) != 3 || r.Logs[0].Topics[0] != hex.EncodeToString(Topic721Transfer) {
		t.Fatalf("transferFrom 事件異常: %v", r.Logs)
	}
	if r.Logs[0].Topics[1] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) ||
		r.Logs[0].Topics[2] != hex.EncodeToString(IntToBytes(addrToInt(bob), 32)) {
		t.Fatalf("transferFrom 事件 from/to 異常: %v", r.Logs[0].Topics)
	}
}

// TestERC721TransferFromNotOwner 驗證非 owner 轉移被拒（REVERT）。
func TestERC721TransferFromNotOwner(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(9)))
	// bob 不是 owner，且 caller 也不是 from → 應 REVERT
	if r := execErc721As(world, rt, bob, Erc721TransferFromCalldata(holder, bob, big.NewInt(9))); r.Success {
		t.Fatal("非 owner 轉移應 REVERT")
	}
	// holder 是 owner 但 caller 是 bob（from=holder）→ caller!=from → REVERT
	if r := execErc721As(world, rt, bob, Erc721TransferFromCalldata(holder, bob, big.NewInt(9))); r.Success {
		t.Fatal("caller!=from 轉移應 REVERT")
	}
}

// TestERC721TokenURI 驗證 tokenURI 未設定時回 0（查詢相容）。
func TestERC721TokenURI(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	if got := queryErc721(t, world, rt, creator, Erc721TokenURICalldata(big.NewInt(1))); got.Sign() != 0 {
		t.Fatalf("未設定 tokenURI 應為 0，得到 %s", got)
	}
}

// TestERC721BalanceOfMultiple 驗證同一 owner 持有多個 token 時 balance 累加。
func TestERC721BalanceOfMultiple(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	for _, id := range []int64{1, 2, 3} {
		if r := execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(id))); !r.Success {
			t.Fatalf("mint %d 失敗: %v", id, r.Err)
		}
	}
	if got := queryErc721(t, world, rt, creator, Erc721BalanceOfCalldata(holder)); got.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("holder balance 應為 3，得到 %s", got)
	}
	if got := queryErc721(t, world, rt, creator, Erc721TotalSupplyCalldata()); got.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("totalSupply 應為 3，得到 %s", got)
	}
}

// ---- M75-1：ERC-721 授權面（approve / setApprovalForAll / getApproved / isApprovedForAll）----

// TestERC721ApproveAndTransfer 驗證 approve(address,uint256)：Approval 事件、getApproved 讀回、
// 被授權 operator 可 transferFrom。
func TestERC721ApproveAndTransfer(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(9)))
	// holder 授權 bob 單一 token 9
	r := execErc721As(world, rt, holder, Erc721ApproveCalldata(bob, big.NewInt(9)))
	if !r.Success {
		t.Fatalf("approve 失敗: %v", r.Err)
	}
	// Approval(holder, bob, 9) 事件
	if len(r.Logs) != 1 || len(r.Logs[0].Topics) != 3 || r.Logs[0].Topics[0] != hex.EncodeToString(Topic721Approval) {
		t.Fatalf("Approval 事件異常: %v", r.Logs)
	}
	if r.Logs[0].Topics[1] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) ||
		r.Logs[0].Topics[2] != hex.EncodeToString(IntToBytes(addrToInt(bob), 32)) {
		t.Fatalf("Approval 事件 owner/approved 異常: %v", r.Logs[0].Topics)
	}
	// getApproved(9) == bob
	if got := queryErc721(t, world, rt, creator, Erc721GetApprovedCalldata(big.NewInt(9))); got.Cmp(addrToInt(bob)) != 0 {
		t.Fatalf("getApproved(9) 應為 bob，得到 %s", got)
	}
	// bob（operator）代 holder 轉移 token 9 給 carol
	carol := "0x" + strings.Repeat("55", 20)
	r2 := execErc721As(world, rt, bob, Erc721TransferFromCalldata(holder, carol, big.NewInt(9)))
	if !r2.Success {
		t.Fatalf("授權 operator transferFrom 應成功: %v", r2.Err)
	}
	if got := queryErc721(t, world, rt, creator, Erc721OwnerOfCalldata(big.NewInt(9))); got.Cmp(addrToInt(carol)) != 0 {
		t.Fatalf("ownerOf(9) 應為 carol，得到 %s", got)
	}
}

// TestERC721ApproveNotOwner 驗證非 owner 且未獲授權者呼叫 approve → REVERT。
func TestERC721ApproveNotOwner(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	eve := "0x" + strings.Repeat("66", 20)
	execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(9)))
	if r := execErc721As(world, rt, eve, Erc721ApproveCalldata(eve, big.NewInt(9))); r.Success {
		t.Fatal("非 owner 未授權 approve 應 REVERT")
	}
}

// TestERC721SetApprovalForAll 驗證 setApprovalForAll：ApprovalForAll 事件、
// isApprovedForAll 讀回、operator 可代轉任意 token。
func TestERC721SetApprovalForAll(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(9)))
	r := execErc721As(world, rt, holder, Erc721SetApprovalForAllCalldata(bob, true))
	if !r.Success {
		t.Fatalf("setApprovalForAll 失敗: %v", r.Err)
	}
	// ApprovalForAll(holder, bob, true) 事件
	if len(r.Logs) != 1 || len(r.Logs[0].Topics) != 3 || r.Logs[0].Topics[0] != hex.EncodeToString(Topic721ApprovalForAll) {
		t.Fatalf("ApprovalForAll 事件異常: %v", r.Logs)
	}
	if r.Logs[0].Topics[1] != hex.EncodeToString(IntToBytes(addrToInt(holder), 32)) ||
		r.Logs[0].Topics[2] != hex.EncodeToString(IntToBytes(addrToInt(bob), 32)) {
		t.Fatalf("ApprovalForAll 事件 owner/operator 異常: %v", r.Logs[0].Topics)
	}
	// isApprovedForAll(holder, bob) == 1
	if got := queryErc721(t, world, rt, creator, Erc721IsApprovedForAllCalldata(holder, bob)); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("isApprovedForAll 應為 1，得到 %s", got)
	}
	// bob 代 holder 轉移（未單獨 approve 的 token 也可轉）
	carol := "0x" + strings.Repeat("55", 20)
	if r2 := execErc721As(world, rt, bob, Erc721TransferFromCalldata(holder, carol, big.NewInt(9))); !r2.Success {
		t.Fatalf("approvalForAll operator transferFrom 應成功: %v", r2.Err)
	}
	if got := queryErc721(t, world, rt, creator, Erc721OwnerOfCalldata(big.NewInt(9))); got.Cmp(addrToInt(carol)) != 0 {
		t.Fatalf("ownerOf(9) 應為 carol，得到 %s", got)
	}
}

// TestERC721SetApprovalForAllRevoke 驗證撤銷後 isApprovedForAll==0 且轉移被拒。
func TestERC721SetApprovalForAllRevoke(t *testing.T) {
	world, rt, creator := deployErc721(t, "MyNFT", "MNFT")
	holder := "0x" + strings.Repeat("22", 20)
	bob := "0x" + strings.Repeat("44", 20)
	execErc721As(world, rt, creator, Erc721MintCalldata(holder, big.NewInt(9)))
	execErc721As(world, rt, holder, Erc721SetApprovalForAllCalldata(bob, true))
	if r := execErc721As(world, rt, holder, Erc721SetApprovalForAllCalldata(bob, false)); !r.Success {
		t.Fatalf("撤銷 setApprovalForAll 失敗: %v", r.Err)
	}
	if got := queryErc721(t, world, rt, creator, Erc721IsApprovedForAllCalldata(holder, bob)); got.Sign() != 0 {
		t.Fatalf("撤銷後 isApprovedForAll 應為 0，得到 %s", got)
	}
	carol := "0x" + strings.Repeat("55", 20)
	if r := execErc721As(world, rt, bob, Erc721TransferFromCalldata(holder, carol, big.NewInt(9))); r.Success {
		t.Fatal("撤銷後 operator transferFrom 應 REVERT")
	}
}
