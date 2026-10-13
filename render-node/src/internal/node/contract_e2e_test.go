package node

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// signedTxWithMemo 構造帶 memo 的完整簽名交易（memo 須在簽名前設置）。
func signedTxWithMemo(t *testing.T, kp *crypto.KeyPair, to, amount, fee string,
	nonce int64, memo string) map[string]any {
	t.Helper()
	tx := map[string]any{
		"from": mustAddr(t, kp), "to": to, "amount": amount, "fee": fee,
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
		"memo":   memo,
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	tx["signature"] = sig
	return tx
}

// buildCounterRuntime 構造「每次調用 slot0+1 並返回新值」的運行時字節碼。
func buildCounterRuntime() []byte {
	code := []byte{
		0x60, 0x00, 0x54, // SLOAD slot0
		0x60, 0x01, 0x01, // +1
		0x60, 0x00, 0x55, // SSTORE slot0（key0 頂、value 次）
		0x60, 0x00, 0x54, // 再 SLOAD
	}
	// 值 MSTORE@0，RETURN(0,32)。
	return append(code, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xF3)
}

// buildInitFor 構造部署字節碼：CODECOPY runtime（源偏移=init 長度12）並 RETURN。
func buildInitFor(runtime []byte) []byte {
	rl := len(runtime)
	init := []byte{
		0x60, byte(rl), // PUSH size
		0x60, 0x0C, // PUSH src offset=12
		0x60, 0x00, // PUSH dest
		0x39, // CODECOPY
		0x60, byte(rl),
		0x60, 0x00,
		0xF3,
	}
	return append(init, runtime...)
}

func TestContractDeployAndCallEndToEnd(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()

	insertGenesisWithAlloc(t, n, alice)
	n.Start()
	defer func() { _ = n.Close() }()

	// 預算合約地址（alice nonce0）。
	alice0x, err := tx0To0x(alice)
	if err != nil {
		t.Fatal(err)
	}
	contract0x := vm.CreateAddress(alice0x, 0)
	contractTx0, err := evmToTx0(contract0x)
	if err != nil {
		t.Fatal(err)
	}

	// 構造部署字節碼（counter 合約）。
	runtime := buildCounterRuntime()
	initCode := buildInitFor(runtime)
	deployMemo := vmDeployPrefix + hex.EncodeToString(initCode)

	deployTx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", 0, deployMemo)
	deployHash, err := n.SubmitTransaction(deployTx)
	if err != nil {
		t.Fatalf("部署交易被拒: %v", err)
	}
	// 等待合約真實部署（code 寫入）——交易存在（含 mempool）不代表已打包執行。
	waitFor(t, func() bool {
		if got, _ := n.DB().GetTransaction(deployHash); got == nil {
			return false
		}
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize == len(runtime)
	}, 15*time.Second, "部署交易未打包或合約未部署")

	// 合約應已部署。
	info := n.contracts.Get(contract0x)
	if info == nil || info.CodeSize != len(runtime) {
		t.Fatalf("合約未正確部署: %+v", info)
	}

	// 兩次調用（nonce1、2），每次 slot0 +1。
	callOnce := func(nonce int64) {
		memo := vmCallPrefix
		tx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", nonce, memo)
		h, err := n.SubmitTransaction(tx)
		if err != nil {
			t.Fatalf("調用交易被拒(nonce %d): %v", nonce, err)
		}
		want := big.NewInt(nonce) // slot0 = nonce（首次部署後 0，第 k 次調用後 k）
		waitFor(t, func() bool {
			if got, _ := n.DB().GetTransaction(h); got == nil {
				return false
			}
			return n.contracts.StorageAt(contract0x, big.NewInt(0)).Cmp(want) == 0
		}, 15*time.Second, "調用交易未打包或 slot 未更新")
	}
	callOnce(1)
	if got := n.contracts.StorageAt(contract0x, big.NewInt(0)); got.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("第一次調用後 slot0 應1, got=%s", got)
	}
	callOnce(2)
	if got := n.contracts.StorageAt(contract0x, big.NewInt(0)); got.Cmp(big.NewInt(2)) != 0 {
		t.Errorf("第二次調用後 slot0 應2, got=%s", got)
	}
}
