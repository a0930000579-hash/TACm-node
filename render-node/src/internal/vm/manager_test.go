package vm

import (
	"encoding/hex"
	"math/big"
	"path/filepath"
	"testing"
)

func TestManagerDeployCallPersist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)

	runtime := runtimeReturn42()
	addr := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(addr, creator, initFor(runtime), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}

	cr, err := m.ApplyCall(addr, creator, nil, big.NewInt(0))
	if err != nil || !cr.OK {
		t.Fatalf("調用失敗: %+v", cr)
	}
	b, _ := hex.DecodeString(cr.ReturnData)
	if BytesToInt(b).Cmp(big.NewInt(42)) != 0 {
		t.Errorf("應返回42 got=%s", cr.ReturnData)
	}

	info := m.Get(addr)
	if info == nil || info.CodeSize != len(runtime) {
		t.Errorf("合約信息錯誤: %+v", info)
	}
	if len(m.List()) != 1 {
		t.Error("應列出1個合約")
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m2, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	cr2, _ := m2.ApplyCall(addr, creator, nil, big.NewInt(0))
	if !cr2.OK {
		t.Fatalf("重開後調用失敗: %s", cr2.Error)
	}
	b2, _ := hex.DecodeString(cr2.ReturnData)
	if BytesToInt(b2).Cmp(big.NewInt(42)) != 0 {
		t.Error("重開後應返回42")
	}
}

func TestManagerCounterStorage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)

	runtime := runtimeCounter()
	addr := CreateAddress(creator, 0)
	dr, _ := m.ApplyDeploy(addr, creator, initFor(runtime), big.NewInt(0))
	if !dr.OK {
		t.Fatalf("counter 部署失敗: %s", dr.Error)
	}

	call := func() *big.Int {
		cr, err := m.ApplyCall(addr, creator, nil, big.NewInt(0))
		if err != nil || !cr.OK {
			t.Fatalf("call 失敗: %+v", cr)
		}
		b, _ := hex.DecodeString(cr.ReturnData)
		return BytesToInt(b)
	}
	if call().Cmp(big.NewInt(1)) != 0 {
		t.Error("第一次應為1")
	}
	if call().Cmp(big.NewInt(2)) != 0 {
		t.Error("第二次應為2")
	}
	m.Close()

	m2, _ := NewContractManager(dbPath)
	defer m2.Close()
	cr, _ := m2.ApplyCall(addr, creator, nil, big.NewInt(0))
	b, _ := hex.DecodeString(cr.ReturnData)
	if BytesToInt(b).Cmp(big.NewInt(3)) != 0 {
		t.Error("重開後應為3，storage 未持久化")
	}
}

// TestManagerSimulateNoLeak 驗證只讀模擬不改變實際存儲（快照回滾徹底）。
func TestManagerSimulateNoLeak(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)
	runtime := runtimeCounter()
	addr := CreateAddress(creator, 0)
	if dr, _ := m.ApplyDeploy(addr, creator, initFor(runtime), big.NewInt(0)); !dr.OK {
		t.Fatalf("部署失敗: %s", dr.Error)
	}
	m.ApplyCall(addr, creator, nil, big.NewInt(0)) // slot1
	m.ApplyCall(addr, creator, nil, big.NewInt(0)) // slot2

	for i := 0; i < 5; i++ {
		cr, err := m.SimulateCall(addr, creator, nil, big.NewInt(0))
		if err != nil || !cr.OK {
			t.Fatalf("sim %d 失敗: %v %+v", i, err, cr)
		}
		rb, _ := hex.DecodeString(cr.ReturnData)
		if BytesToInt(rb).Cmp(big.NewInt(3)) != 0 {
			t.Errorf("sim %d 應基於 slot2 返回3, got=%s", i, BytesToInt(rb))
		}
	}
	if got := m.StorageAt(addr, big.NewInt(0)); got.Cmp(big.NewInt(2)) != 0 {
		t.Errorf("模擬泄漏存儲: slot=%s 應為2", got)
	}
}

// TestInfiniteLoopStoppedByGas 驗證無限迴圈合約被 gas 上限中止（防 DoS）。
func TestInfiniteLoopStoppedByGas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm_loop.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)

	// 迴圈 bytecode：JUMPDEST, PUSH1 0, JUMP → 無限跳回 0。
	loop := []byte{0x5B, 0x60, 0x00, 0x56}
	addr := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(addr, creator, initFor(loop), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	cr, err := m.ApplyCall(addr, creator, nil, big.NewInt(0))
	if err != nil {
		t.Fatal(err)
	}
	if cr.OK {
		t.Fatal("無限迴圈應被 gas 中止，卻成功返回")
	}
	if !cr.Reverted {
		t.Fatal("OOG 應標記 Reverted")
	}
}

// TestApplyCallRollbackOnOOG 驗證 OOG 時已寫 storage 必須回滾（EVM 原子性）。
func TestApplyCallRollbackOnOOG(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm_rb.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)

	// code：PUSH1 1, PUSH1 0, SSTORE（寫 slot0=1）→ 迴圈到 OOG。
	code := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x5B, 0x60, 0x00, 0x56}
	addr := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(addr, creator, initFor(code), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	cr, err := m.ApplyCall(addr, creator, nil, big.NewInt(0))
	if err != nil {
		t.Fatal(err)
	}
	if cr.OK {
		t.Fatal("迴圈應 OOG")
	}
	// 驗證 storage 已回滾（不應殘留任何非零槽）。
	acc := m.world.accounts[NormalizeAddress(addr)]
	if acc != nil {
		for k, v := range acc.Storage {
			if v.Sign() != 0 {
				t.Fatalf("OOG 後 storage 未回滾: slot=%s value=%v", k, v)
			}
		}
	}
}

// TestApplyDeployRollbackOnOOG 驗證部署 OOG 時初始化寫入的 storage 必須回滾。
func TestApplyDeployRollbackOnOOG(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm_deployrb.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)

	// initCode：PUSH1 1, PUSH1 0, SSTORE（寫 slot0=1）→ 迴圈到 OOG。
	init := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x5B, 0x60, 0x00, 0x56}
	addr := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(addr, creator, init, big.NewInt(0))
	if err != nil {
		t.Fatal(err)
	}
	if dr.OK {
		t.Fatal("部署應 OOG 失敗")
	}
	// 合約不應註冊，且 storage 乾淨。
	if m.Get(addr) != nil {
		t.Fatal("OOG 部署不應註冊合約")
	}
	acc := m.world.accounts[NormalizeAddress(addr)]
	if acc != nil {
		for k, v := range acc.Storage {
			if v.Sign() != 0 {
				t.Fatalf("OOG 部署後 storage 殘留: slot=%s value=%v", k, v)
			}
		}
	}
}

// TestApplyCallGasLimit 驗證 RPC gas_limit 生效：gas 過小即使正常合約也會 OOG。
func TestApplyCallGasLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm_gaslimit.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)

	runtime := runtimeReturn42()
	addr := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(addr, creator, initFor(runtime), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	// gas=100 過小（PUSH/SSTORE 等不足）：應 OOG。
	cr, err := m.ApplyCallGas(addr, creator, nil, big.NewInt(0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if cr.OK {
		t.Fatal("gas_limit=10 應 OOG 失敗")
	}
	// 預設 gas 下正常成功。
	cr2, err := m.ApplyCall(addr, creator, nil, big.NewInt(0))
	if err != nil || !cr2.OK {
		t.Fatalf("預設 gas 應成功: %+v err=%v", cr2, err)
	}
}

// M151c：eth_call 直接呼叫 precompile 0x09（VRF）——無需合約碼。
func TestManagerCallVRFPrecompileDirect(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	cr, err := m.SimulateCallGas("0x0000000000000000000000000000000000000009",
		"0x0000000000000000000000000000000000000000", []byte{0x11}, big.NewInt(0), m.defaultGas)
	if err != nil {
		t.Fatal(err)
	}
	if !cr.OK || cr.Reverted {
		t.Fatalf("VRF precompile 直接呼叫應成功: %+v", cr)
	}
	if len(cr.ReturnData) != 64 { // hex 32 bytes
		t.Fatalf("VRF 輸出應 32 bytes（hex 64 字元）: %d", len(cr.ReturnData))
	}
	// 非 precompile 無 code 地址仍應「contract not deployed」。
	cr2, _ := m.SimulateCallGas("0x000000000000000000000000000000000000000a",
		"0x0000000000000000000000000000000000000000", []byte{0x11}, big.NewInt(0), m.defaultGas)
	if cr2.OK || cr2.Error != "contract not deployed" {
		t.Fatalf("0x0a 無 code 應 contract not deployed: %+v", cr2)
	}
}

// TestSimulateCallGasOverrides：eth_call state override——storage/balance/nonce 臨時覆蓋
// 影響模擬結果，模擬結束後一律還原（hardhat/Foundry 依賴語義）。
func TestSimulateCallGasOverrides(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tac_evm.db")
	m, err := NewContractManager(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	creator := "0x00000000000000000000000000000000000000a0"
	m.SyncAccount(creator, big.NewInt(1000), 0)
	// 部署「讀 storage[calldata[4..36]]」合約（erc20BalanceOfCode 模式）。
	addr := CreateAddress(creator, 0)
	if dr, err := m.ApplyDeploy(addr, creator, initFor(erc20BalanceOfCode), big.NewInt(0)); err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	slotKey := big.NewInt(5)
	calldata := make([]byte, 36)
	slotKey.FillBytes(calldata[4:])

	// 無 override：storage[5] = 0。
	cr, err := m.SimulateCallGasOverrides(addr, creator, calldata, big.NewInt(0), m.DefaultGas(), nil)
	if err != nil || !cr.OK {
		t.Fatalf("正常模擬失敗: %+v err=%v", cr, err)
	}
	if v := new(big.Int).SetBytes(mustDecodeHex(t, cr.ReturnData)); v.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("無 override 期望 0, got %v", v)
	}

	// override：balance/nonce/state（slot5=0x2a）。
	overrides := map[string]map[string]any{
		addr: {
			"balance": "0x10",
			"nonce":   "0x1",
			"state":   map[string]any{"0x5": "0x2a"},
		},
	}
	cr2, err := m.SimulateCallGasOverrides(addr, creator, calldata, big.NewInt(0), m.DefaultGas(), overrides)
	if err != nil || !cr2.OK {
		t.Fatalf("override 模擬失敗: %+v err=%v", cr2, err)
	}
	if v := new(big.Int).SetBytes(mustDecodeHex(t, cr2.ReturnData)); v.Cmp(big.NewInt(0x2a)) != 0 {
		t.Fatalf("override 未生效, got %v", v)
	}

	// 模擬結束後狀態還原（storage/balance 均回原值）。
	if m.world.GetStorage(addr, slotKey).Cmp(big.NewInt(0)) != 0 {
		t.Fatal("storage override 未還原")
	}
	if m.world.GetBalance(addr).Cmp(big.NewInt(0)) != 0 {
		t.Fatal("balance override 未還原")
	}
	if m.world.GetNonce(addr) != 0 {
		t.Fatal("nonce override 未還原")
	}
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解碼失敗 %q: %v", s, err)
	}
	return b
}
