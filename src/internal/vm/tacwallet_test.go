package vm

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"

	"tacm/internal/crypto"
)

// P1-② 多簽合約錢包（TACWallet）單測：owner 管理、門檻、EIP-1271 簽名驗證、
// approve 收集達門檻後 CALL 執行、防重複、查詢面。

func deployTACWallet(t *testing.T) (*WorldState, []byte, string, string) {
	t.Helper()
	in, world := newTestVM(0)
	creator := "0x" + strings.Repeat("11", 20)
	walletAddr := "0x" + strings.Repeat("aa", 20)
	in.ctx.Caller = creator
	in.ctx.Address = walletAddr
	res := in.Execute(TACWalletInit(), nil)
	if !res.Success {
		t.Fatalf("TACWallet 部署失敗: %v", res.Err)
	}
	if len(res.ReturnData) == 0 {
		t.Fatal("部署未回傳 runtime")
	}
	if string(res.ReturnData) != string(TACWalletRuntime()) {
		t.Fatal("部署回傳 runtime 與 TACWalletRuntime() 不一致")
	}
	return world, res.ReturnData, creator, walletAddr
}

func execWallet(world *WorldState, runtime []byte, caller, addr string, calldata []byte) *ExecutionResult {
	ctx := NewContext()
	ctx.Gas = 10_000_000
	ix := NewInterpreter(ctx, world, 0, false)
	ix.ctx.Caller = caller
	ix.ctx.Address = addr
	return ix.Execute(runtime, calldata)
}

// ownerAddr 把 owner 20-byte bytes 轉 0x 字串呼叫者。
func ownerAddr(owner []byte) string {
	return "0x" + hex.EncodeToString(owner)
}

// walletCD 組裝「selector + 固定 32-byte 參數」calldata。
func walletCD(sel []byte, args ...*big.Int) []byte {
	b := append([]byte{}, sel...)
	for _, a := range args {
		b = append(b, IntToBytes(a, 32)...)
	}
	return b
}

// walletOwner 生成確定性 owner 密鑰對＋Hash160 地址 bytes（與 ecrecover 回傳一致）。
func walletOwner(t *testing.T, seed int) (*btcec.PrivateKey, []byte) {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("生成密鑰失敗: %v", err)
	}
	if seed > 0 {
		// 確定性：由 seed 反覆 hash 得到私鑰 bytes
		b := []byte{byte(seed), byte(seed >> 8), byte(seed >> 16), byte(seed >> 24)}
		h := crypto.Keccak256(b, []byte("wallet-owner"))
		priv, _ = btcec.PrivKeyFromBytes(h)
	}
	ownerBytes := crypto.Hash160(priv.PubKey().SerializeCompressed())
	return priv, ownerBytes
}

// owner32 把 20-byte owner bytes 左補為 32-byte calldata 值。
func owner32(owner []byte) *big.Int { return BytesToInt(owner) }

// signTxHash 對 txHash 簽名，回 (v, r, s) 各 32-byte 值。
func signTxHash(t *testing.T, priv *btcec.PrivateKey, txHash []byte) (*big.Int, *big.Int, *big.Int) {
	t.Helper()
	sig, err := crypto.SignEthereumMessage(txHash, priv.Serialize())
	if err != nil {
		t.Fatalf("簽名失敗: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("簽名長度異常 %d", len(sig))
	}
	return big.NewInt(int64(sig[0])), BytesToInt(sig[1:33]), BytesToInt(sig[33:65])
}

func TestWalletDeployAndOwnerManage(t *testing.T) {
	world, runtime, creator, addr := deployTACWallet(t)
	priv, ownerB := walletOwner(t, 0)
	owner := owner32(ownerB)

	// 非部署者 addOwner → revert
	r := execWallet(world, runtime, "0x" + strings.Repeat("22", 20), addr, walletCD(SelectorWalletAddOwner, owner))
	if r.Success {
		t.Fatal("非部署者 addOwner 應 revert")
	}
	// 部署者 addOwner
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, owner))
	if !r.Success {
		t.Fatalf("addOwner 失敗: %v", r.Err)
	}
	// ownerCount = 1
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletOwnerCount))
	if !r.Success || retInt(r).Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("ownerCount 應為 1，got %v", retInt(r))
	}
	// isOwner true
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletIsOwner, owner))
	if !r.Success || retInt(r).Sign() != 1 {
		t.Fatalf("isOwner 應為 1，got %v", retInt(r))
	}
	// 重複 addOwner → revert
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, owner))
	if r.Success {
		t.Fatal("重複 addOwner 應 revert")
	}
	// 第二 owner＋threshold=2
	priv2, owner2B := walletOwner(t, 1)
	owner2 := owner32(owner2B)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, owner2))
	if !r.Success {
		t.Fatalf("addOwner2 失敗: %v", r.Err)
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletChangeThreshold, big.NewInt(2)))
	if !r.Success {
		t.Fatalf("changeThreshold 失敗: %v", r.Err)
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletThreshold))
	if !r.Success || retInt(r).Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("threshold 應為 2，got %v", retInt(r))
	}

	// removeOwner：移除 owner2 → threshold 校正為 1
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletRemoveOwner, owner2))
	if !r.Success {
		t.Fatalf("removeOwner 失敗: %v", r.Err)
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletOwnerCount))
	if retInt(r).Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("ownerCount 應為 1，got %v", retInt(r))
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletThreshold))
	if retInt(r).Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("threshold 應校正為 1，got %v", retInt(r))
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletIsOwner, owner2))
	if !r.Success || retInt(r).Sign() != 0 {
		t.Fatalf("owner2 應已移除，got %v", retInt(r))
	}
	_ = priv
	_ = priv2
}

func TestWalletChangeThresholdEdge(t *testing.T) {
	world, runtime, creator, addr := deployTACWallet(t)
	// threshold=0 → revert
	r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletChangeThreshold, big.NewInt(0)))
	if r.Success {
		t.Fatal("threshold=0 應 revert")
	}
	// threshold=5（>count=0）→ revert
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletChangeThreshold, big.NewInt(5)))
	if r.Success {
		t.Fatal("threshold>count 應 revert")
	}
	// 非部署者 → revert
	r = execWallet(world, runtime, "0x" + strings.Repeat("22", 20), addr, walletCD(SelectorWalletChangeThreshold, big.NewInt(1)))
	if r.Success {
		t.Fatal("非部署者 changeThreshold 應 revert")
	}
}

func TestWalletSubmitOwnerOnly(t *testing.T) {
	world, runtime, creator, addr := deployTACWallet(t)
	priv, ownerB := walletOwner(t, 0)
	owner := owner32(ownerB)
	r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, owner))
	if !r.Success {
		t.Fatalf("addOwner 失敗: %v", r.Err)
	}
	to := big.NewInt(0x1234)
	value := big.NewInt(100)
	nonce := big.NewInt(0)
	// 非 owner submit → revert
	r = execWallet(world, runtime, "0x" + strings.Repeat("22", 20), addr, walletCD(SelectorWalletSubmit, to, value, nonce))
	if r.Success {
		t.Fatal("非 owner submit 應 revert")
	}
	// owner submit → txHash 回傳（32 bytes 非零）
	r = execWallet(world, runtime, ownerAddr(ownerB), addr, walletCD(SelectorWalletSubmit, to, value, nonce))
	if !r.Success {
		t.Fatalf("submit 失敗: %v", r.Err)
	}
	if len(r.ReturnData) != 32 {
		t.Fatalf("submit 應回傳 32 bytes，got %d", len(r.ReturnData))
	}
	_ = priv
}

func TestWalletApproveFlow(t *testing.T) {
	world, runtime, creator, addr := deployTACWallet(t)
	priv1, owner1B := walletOwner(t, 0)
	priv2, owner2B := walletOwner(t, 1)
	priv3, owner3B := walletOwner(t, 2)
	o1 := owner32(owner1B)
	o2 := owner32(owner2B)
	o3 := owner32(owner3B)
	for _, o := range []*big.Int{o1, o2, o3} {
		r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, o))
		if !r.Success {
			t.Fatalf("addOwner 失敗: %v", r.Err)
		}
	}
	r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletChangeThreshold, big.NewInt(2)))
	if !r.Success {
		t.Fatalf("changeThreshold 失敗: %v", r.Err)
	}

	// 錢包給 1000 餘額
	world.SetBalance(addr, big.NewInt(1000))
	to := big.NewInt(0x1234)
	value := big.NewInt(400)
	nonce := big.NewInt(0)
	txHash := Keccak256(IntToBytes(to, 32), IntToBytes(value, 32), IntToBytes(nonce, 32))

	// owner1 approve → 未達門檻（approvals=1）
	v, rv, sv := signTxHash(t, priv1, txHash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprove, to, value, nonce, v, rv, sv))
	if !r.Success {
		t.Fatalf("approve1 失敗: %v", r.Err)
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprovals, to, value, nonce))
	if !r.Success || retInt(r).Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("approvals 應為 1，got %v", retInt(r))
	}
	// 錢包餘額未動
	if world.GetBalance(addr).Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("未達門檻不應執行轉帳，餘額 %v", world.GetBalance(addr))
	}

	// owner1 重複 approve → revert
	v, rv, sv = signTxHash(t, priv1, txHash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprove, to, value, nonce, v, rv, sv))
	if r.Success {
		t.Fatal("重複 approve 應 revert")
	}
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletIsApproved, to, value, nonce, o1))
	if !r.Success || retInt(r).Sign() != 1 {
		t.Fatalf("isApproved(o1) 應為 1，got %v", retInt(r))
	}

	// 非 owner 簽名 approve → revert（owner3 未加入？—— 已加入—— 用隨機密鑰）
	privX, _ := walletOwner(t, 9)
	v, rv, sv = signTxHash(t, privX, txHash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprove, to, value, nonce, v, rv, sv))
	if r.Success {
		t.Fatal("非 owner 簽名 approve 應 revert")
	}

	// owner2 approve → 達門檻 → CALL(to, 400) 執行
	v, rv, sv = signTxHash(t, priv2, txHash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprove, to, value, nonce, v, rv, sv))
	if !r.Success {
		t.Fatalf("approve2 失敗: %v", r.Err)
	}
	// 目標收到 400
	if world.GetBalance(to).Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("執行後目標餘額應為 400，got %v", world.GetBalance(to))
	}
	// 錢包餘額 600
	if world.GetBalance(addr).Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("執行後錢包餘額應為 600，got %v", world.GetBalance(addr))
	}
	// nonce++（slot3）=1：submit 回傳 txHash 不同 nonce—— 直接查 slot3
	if world.GetStorage(addr, big.NewInt(3)).Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("nonce 應為 1，got %v", world.GetStorage(addr, big.NewInt(3)))
	}
	_ = priv3
}

func TestWalletApproveInsufficientBalanceReverts(t *testing.T) {
	world, runtime, creator, addr := deployTACWallet(t)
	priv1, owner1B := walletOwner(t, 0)
	priv2, owner2B := walletOwner(t, 1)
	o1 := owner32(owner1B)
	o2 := owner32(owner2B)
	for _, o := range []*big.Int{o1, o2} {
		r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, o))
		if !r.Success {
			t.Fatalf("addOwner 失敗: %v", r.Err)
		}
	}
	r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletChangeThreshold, big.NewInt(2)))
	if !r.Success {
		t.Fatalf("changeThreshold 失敗: %v", r.Err)
	}
	// 錢包無餘額
	to := big.NewInt(0x1234)
	value := big.NewInt(400)
	nonce := big.NewInt(0)
	txHash := Keccak256(IntToBytes(to, 32), IntToBytes(value, 32), IntToBytes(nonce, 32))
	v, rv, sv := signTxHash(t, priv1, txHash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprove, to, value, nonce, v, rv, sv))
	if !r.Success {
		t.Fatalf("approve1 失敗: %v", r.Err)
	}
	// owner2 達門檻 → CALL 餘額不足 → 整筆 revert
	v, rv, sv = signTxHash(t, priv2, txHash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletApprove, to, value, nonce, v, rv, sv))
	if r.Success {
		t.Fatal("餘額不足執行應 revert")
	}
	// 目標無收款
	if world.GetBalance(to).Sign() != 0 {
		t.Fatalf("目標餘額應為 0，got %v", world.GetBalance(to))
	}
}

func TestWalletIsValidSignature(t *testing.T) {
	world, runtime, creator, addr := deployTACWallet(t)
	priv, ownerB := walletOwner(t, 0)
	owner := owner32(ownerB)
	r := execWallet(world, runtime, creator, addr, walletCD(SelectorWalletAddOwner, owner))
	if !r.Success {
		t.Fatalf("addOwner 失敗: %v", r.Err)
	}
	hash := crypto.Keccak256([]byte("TAC message"))
	v, rv, sv := signTxHash(t, priv, hash)
	// owner 簽名 → magic value 0x1626ba7e
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletIsValidSig, BytesToInt(hash), v, rv, sv))
	if !r.Success {
		t.Fatalf("isValidSignature 失敗: %v", r.Err)
	}
	if len(r.ReturnData) != 32 {
		t.Fatalf("回傳應為 32 bytes，got %d", len(r.ReturnData))
	}
	// 前 4 bytes = 0x1626ba7e
	if r.ReturnData[0] != 0x16 || r.ReturnData[1] != 0x26 || r.ReturnData[2] != 0xba || r.ReturnData[3] != 0x7e {
		t.Fatalf("magic value 錯誤: %x", r.ReturnData[:4])
	}
	// 非 owner 簽名 → revert
	privX, _ := walletOwner(t, 8)
	v, rv, sv = signTxHash(t, privX, hash)
	r = execWallet(world, runtime, creator, addr, walletCD(SelectorWalletIsValidSig, BytesToInt(hash), v, rv, sv))
	if r.Success {
		t.Fatal("非 owner 簽名 isValidSignature 應 revert")
	}
}
