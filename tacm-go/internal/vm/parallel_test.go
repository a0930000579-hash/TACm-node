package vm

import (
	"encoding/hex"
	"math/big"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 本檔驗證區塊內 EVM 並行執行（對標 Sei / Block-STM）與串行結果完全一致：
//   - TestParallelERC20SerialEquivalence：N 筆互不相干使用者的獨立轉帳（同並行 layer）
//     ＋ 同 sender 衝突交易混合，分別以純串行 manager 與並行 manager 執行，逐筆比對
//     OK/GasUsed/ReturnData/Logs，並比對最終各持幣人 storage 餘額。
//   - TestParallelCallFamilyGoesSerial：含 CALL-family opcode 合約自動落串行。
//   - TestParallelUndecodableCalldataGoesSerial：無法解碼 calldata 自動落串行。
//   - TestParallelSharedSlotAbortsAndFallsBack：同 slot 寫入衝突觸發 abort→串行回退，
//     且回退後結果仍正確。

func newParallelTestManager(t *testing.T) *ContractManager {
	t.Helper()
	m, err := NewContractManager(filepath.Join(t.TempDir(), "evm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func paddr(pair string) string { return "0x" + strings.Repeat(pair, 20) }

// deployTwoERC20 把同一 ERC20（totalSupply）部署到兩個獨立 manager，回傳合約位址。
func deployTwoERC20(t *testing.T, supply int64) (serialM, parM *ContractManager, contract, creator string) {
	t.Helper()
	creator = paddr("a0")
	serialM = newParallelTestManager(t)
	parM = newParallelTestManager(t)
	contract = CreateAddress(creator, 0)
	for _, m := range []*ContractManager{serialM, parM} {
		m.SyncAccount(creator, big.NewInt(supply), 0)
		dr, err := m.ApplyDeploy(contract, creator, Erc20Init(big.NewInt(supply)), big.NewInt(0))
		if err != nil || !dr.OK {
			t.Fatalf("部署 ERC20 失敗: %+v err=%v", dr, err)
		}
	}
	return serialM, parM, contract, creator
}

func parallelBalanceOf(t *testing.T, m *ContractManager, contract, holder string) *big.Int {
	t.Helper()
	cr, err := m.SimulateCall(contract, ZeroAddress, Erc20BalanceOfCalldata(holder), big.NewInt(0))
	if err != nil || !cr.OK {
		t.Fatalf("balanceOf(%s) 失敗: %+v err=%v", holder, cr, err)
	}
	b, _ := hex.DecodeString(cr.ReturnData)
	return BytesToInt(b)
}

func parallelFund(t *testing.T, m *ContractManager, contract, from, to string, amt int64) {
	t.Helper()
	cr, err := m.ApplyCall(contract, from, Erc20TransferCalldata(to, big.NewInt(amt)), big.NewInt(0))
	if err != nil || !cr.OK {
		t.Fatalf("fund %s->%s 失敗: %+v err=%v", from, to, cr, err)
	}
}

func TestParallelERC20SerialEquivalence(t *testing.T) {
	const supply = 1_000_000
	serialM, parM, contract, creator := deployTwoERC20(t, supply)

	A, B, C := paddr("11"), paddr("22"), paddr("33")
	X, Y, Z, W := paddr("44"), paddr("55"), paddr("66"), paddr("77")

	// 前置：給 A/B/C 各 500（兩邊都做，確保並行段落起點狀態一致）。
	for _, m := range []*ContractManager{serialM, parM} {
		parallelFund(t, m, contract, creator, A, 500)
		parallelFund(t, m, contract, creator, B, 500)
		parallelFund(t, m, contract, creator, C, 500)
	}

	// 並行段落：
	//   tx0 A->X 200、tx1 B->Y 200、tx2 C->Z 200（外部集合互斥 → 同一並行 layer）
	//   tx3 A->W 100（同 sender A → 與 tx0 衝突 → 下一 layer）
	items := []ParallelCallItem{
		{To0x: contract, From0x: A, Calldata: Erc20TransferCalldata(X, big.NewInt(200))},
		{To0x: contract, From0x: B, Calldata: Erc20TransferCalldata(Y, big.NewInt(200))},
		{To0x: contract, From0x: C, Calldata: Erc20TransferCalldata(Z, big.NewInt(200))},
		{To0x: contract, From0x: A, Calldata: Erc20TransferCalldata(W, big.NewInt(100))},
	}

	// 串行基準。
	serialRes := make([]*CallResult, len(items))
	for i := range items {
		it := &items[i]
		serialM.SyncAccount(it.From0x, big.NewInt(0), 0)
		cr, err := serialM.ApplyCall(it.To0x, it.From0x, it.Calldata, big.NewInt(0))
		if err != nil {
			t.Fatal(err)
		}
		serialRes[i] = cr
	}

	// 並行。
	for _, it := range items {
		parM.SyncAccount(it.From0x, big.NewInt(0), 0)
		parM.SyncAccount(it.To0x, big.NewInt(0), 0)
	}
	parRes, fellBack, err := parM.ApplyCallsParallel(items)
	if err != nil {
		t.Fatal(err)
	}
	if fellBack {
		t.Fatal("獨立 ERC20 轉帳不應觸發串行回退")
	}

	// 逐筆比對 OK/GasUsed/ReturnData/Logs。
	for i := range items {
		s, p := serialRes[i], parRes[i]
		if s.OK != p.OK {
			t.Errorf("tx%d OK 不一致: serial=%v parallel=%v", i, s.OK, p.OK)
		}
		if s.GasUsed != p.GasUsed {
			t.Errorf("tx%d GasUsed 不一致: serial=%d parallel=%d", i, s.GasUsed, p.GasUsed)
		}
		if s.ReturnData != p.ReturnData {
			t.Errorf("tx%d ReturnData 不一致: serial=%s parallel=%s", i, s.ReturnData, p.ReturnData)
		}
		if !reflect.DeepEqual(s.Logs, p.Logs) {
			t.Errorf("tx%d Logs 不一致:\n serial=%v\n parallel=%v", i, s.Logs, p.Logs)
		}
	}

	// 最終 storage/餘額逐項比較。
	for _, h := range []string{creator, A, B, C, X, Y, Z, W} {
		sb := parallelBalanceOf(t, serialM, contract, h)
		pb := parallelBalanceOf(t, parM, contract, h)
		if sb.Cmp(pb) != 0 {
			t.Errorf("holder %s 餘額不一致: serial=%s parallel=%s", h, sb, pb)
		}
	}
}

func TestParallelCallFamilyGoesSerial(t *testing.T) {
	m := newParallelTestManager(t)
	creator := paddr("a0")
	m.SyncAccount(creator, big.NewInt(1000), 0)
	contract := CreateAddress(creator, 0)
	// runtime 含 CALL opcode（0xF1）。
	runtime := []byte{0xF1, 0x00}
	dr, err := m.ApplyDeploy(contract, creator, initFor(runtime), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	code := m.GetCode(contract)
	elig, _ := CallParallelEligible(code, creator, contract,
		Erc20TransferCalldata(paddr("22"), big.NewInt(1)))
	if elig {
		t.Error("含 CALL opcode 的合約應自動落入串行 lane")
	}
}

func TestParallelUndecodableCalldataGoesSerial(t *testing.T) {
	m := newParallelTestManager(t)
	creator := paddr("a0")
	m.SyncAccount(creator, big.NewInt(1_000_000), 0)
	contract := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(contract, creator, Erc20Init(big.NewInt(1_000_000)), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	code := m.GetCode(contract)
	// 未知 selector → 不可並行。
	elig, _ := CallParallelEligible(code, creator, contract, []byte{0xde, 0xad, 0xbe, 0xef})
	if elig {
		t.Error("無法解碼 calldata（未知 selector）應自動落入串行 lane")
	}
	// calldata 過短 → 不可並行。
	elig2, _ := CallParallelEligible(code, creator, contract, []byte{0xa9, 0x05})
	if elig2 {
		t.Error("過短 calldata 應自動落入串行 lane")
	}
}

// runtimeCounterInc 為「忽略 calldata、每次把 slot0 +1 並回傳新值」的運行時碼。
// 用來人為構造「兩個不同 caller 都寫同一全局 slot」的未建模寫入衝突。
var runtimeCounterInc = []byte{
	0x60, 0x00, // PUSH1 0
	0x54,       // SLOAD slot0
	0x60, 0x01, // PUSH1 1
	0x01,       // ADD
	0x60, 0x00, // PUSH1 0
	0x55,       // SSTORE slot0
	0x60, 0x00, // PUSH1 0
	0x54,             // SLOAD slot0
	0x60, 0x00, 0x52, // MSTORE@0
	0x60, 0x20, 0x60, 0x00, 0xF3, // RETURN(0,32)
}

func TestParallelSharedSlotAbortsAndFallsBack(t *testing.T) {
	m := newParallelTestManager(t)
	creator := paddr("a0")
	A, B, to1, to2 := paddr("11"), paddr("22"), paddr("33"), paddr("44")
	m.SyncAccount(creator, big.NewInt(1000), 0)
	contract := CreateAddress(creator, 0)
	dr, err := m.ApplyDeploy(contract, creator, initFor(runtimeCounterInc), big.NewInt(0))
	if err != nil || !dr.OK {
		t.Fatalf("部署失敗: %+v err=%v", dr, err)
	}
	m.SyncAccount(A, big.NewInt(100), 0)
	m.SyncAccount(B, big.NewInt(100), 0)

	// 兩筆：不同 caller、解碼出不同 recipient → 預測外部集合互斥 → 同一 layer。
	// 但二者都寫 contract.slot0 → 事後同 slot 衝擊檢測應 abort→fellBack。
	items := []ParallelCallItem{
		{To0x: contract, From0x: A, Calldata: Erc20TransferCalldata(to1, big.NewInt(1))},
		{To0x: contract, From0x: B, Calldata: Erc20TransferCalldata(to2, big.NewInt(1))},
	}
	res, fellBack, err := m.ApplyCallsParallel(items)
	if err != nil {
		t.Fatal(err)
	}
	if !fellBack {
		t.Fatalf("同 slot 寫入衝突應觸發 abort→fellBack, got res=%+v", res)
	}
	// 串行重放：slot0 應等於 2（與舊行為逐字一致）。
	for i := range items {
		cr, rerr := m.ApplyCall(items[i].To0x, items[i].From0x, items[i].Calldata, big.NewInt(0))
		if rerr != nil || !cr.OK {
			t.Fatalf("串行重放失敗: %+v err=%v", cr, rerr)
		}
	}
	if got := m.StorageAt(contract, big.NewInt(0)); got.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("回退串行後 slot0 應為 2, got=%s", got)
	}
}
