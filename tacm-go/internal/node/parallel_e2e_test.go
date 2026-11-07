package node

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// TestParallelBlockContractsE2E 驗證區塊合約執行走「並行優先、串行兜底」管線：
// 部署 ERC20 後連續提交多筆 transfer 呼叫，等待結帳後斷言最終持幣餘額正確。
// 真正的「並行 vs 串行逐字一致」已在 internal/vm/parallel_test.go 證明；本檔驗證
// 節點層 executeBlockContractsParallel 切段、同步、持久化與回退管線不破壞行為。
func TestParallelBlockContractsE2E(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, err := aliceKP.Address()
	if err != nil {
		t.Fatal(err)
	}
	insertGenesisWithAlloc(t, n, alice)
	n.Start()
	defer func() { _ = n.Close() }()

	alice0x, err := tx0To0x(alice)
	if err != nil {
		t.Fatal(err)
	}
	contract0x := vm.CreateAddress(alice0x, 0)
	contractTx0, err := evmToTx0(contract0x)
	if err != nil {
		t.Fatal(err)
	}

	// 部署 ERC20（totalSupply=1,000,000 全歸 alice）。
	initCode := vm.Erc20Init(big.NewInt(1_000_000))
	deployMemo := vmDeployPrefix + hex.EncodeToString(initCode)
	deployTx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", 0, deployMemo)
	if _, err := n.SubmitTransaction(deployTx); err != nil {
		t.Fatalf("部署交易被拒: %v", err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 15*time.Second, "ERC20 未部署")

	// 連續送出多筆 transfer（alice → 4 個不同 recipient）。
	recips := []string{"0x1111111111111111111111111111111111111111",
		"0x2222222222222222222222222222222222222222",
		"0x3333333333333333333333333333333333333333",
		"0x4444444444444444444444444444444444444444"}
	const amt = int64(50_000)
	hashes := make([]string, 0, len(recips))
	nonce := int64(1) // 0 已用於部署
	for _, r := range recips {
		calldata := vm.Erc20TransferCalldata(r, big.NewInt(amt))
		memo := vmCallPrefix + hex.EncodeToString(calldata)
		tx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", nonce, memo)
		h, err := n.SubmitTransaction(tx)
		if err != nil {
			t.Fatalf("transfer 交易被拒(nonce %d): %v", nonce, err)
		}
		hashes = append(hashes, h)
		nonce++
	}

	// 等待全部交易結帳。
	waitFor(t, func() bool {
		for _, h := range hashes {
			got, _ := n.DB().GetTransaction(h)
			if got == nil {
				return false
			}
		}
		return true
	}, 20*time.Second, "transfer 交易未全部結帳")

	// 斷言各 recipient 餘額 = amt；alice 餘額 = 1,000,000 - 4*amt。
	balOf := func(holder string) *big.Int {
		cr, err := n.contracts.SimulateCall(contract0x, holder,
			vm.Erc20BalanceOfCalldata(holder), big.NewInt(0))
		if err != nil || !cr.OK {
			t.Fatalf("balanceOf(%s) 失敗: %+v err=%v", holder, cr, err)
		}
		b, _ := hex.DecodeString(cr.ReturnData)
		return vm.BytesToInt(b)
	}
	for _, r := range recips {
		if got := balOf(r); got.Cmp(big.NewInt(amt)) != 0 {
			t.Errorf("recipient %s 餘額應 %d, got %s", r, amt, got)
		}
	}
	if got := balOf(alice0x); got.Cmp(big.NewInt(1_000_000-4*amt)) != 0 {
		t.Errorf("alice 餘額應 %d, got %s", 1_000_000-4*amt, got)
	}
}
