package node

import (
	"math"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// TestEIP1559DynamicFee 驗證 EIP-1559 動態手續費閉環：
//  1. EIP-1559 交易（max_fee/priority_fee/gas_limit）入池並出塊；
//  2. 區塊帶 base_fee/gas_used/gas_limit/burned；
//  3. 交易費用拆分：burn（銷毀）＋tip（出塊者）；
//  4. 全鏈審計守恆：總餘額＋銷毀 == coinbase 增發。
func TestEIP1559DynamicFee(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, aliceKP)
	insertGenesisWithAlloc(t, n, alice)
	bobKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bob := mustAddr(t, bobKP)
	n.Start()
	defer n.Close()

	// 送一筆 EIP-1559 轉帳：fee = base×gas 保守值 0.021、max_fee 上限 0.0001/gas。
	tx := signedTx(t, aliceKP, bob, "10", "0.021", 0)
	tx["max_fee"] = "0.0001"
	tx["priority_fee"] = "0.000000001"
	tx["gas_limit"] = "21000"
	txHash, err := n.SubmitTransaction(tx)
	if err != nil {
		t.Fatalf("EIP-1559 tx 提交失敗: %v", err)
	}
	t.Logf("eip1559 tx=%s", txHash)

	// 等出塊。
	waitFor(t, func() bool { return n.db.GetTipHeight() >= 1 }, 8*time.Second, "EIP-1559 tx 未出塊")

	// 區塊帶 EIP-1559 字段。
	b, err := n.db.GetBlock(1)
	if err != nil || b == nil {
		t.Fatalf("讀取區塊 1 失敗: %v", err)
	}
	if b.BaseFee == "" {
		t.Fatal("區塊缺少 base_fee")
	}
	if b.GasUsed < 21000 {
		t.Fatalf("gas_used 應 ≥21000，得到 %d", b.GasUsed)
	}
	if b.GasLimit != chaindb.BlockGasLimit {
		t.Fatalf("gas_limit 應為 %d，得到 %d", chaindb.BlockGasLimit, b.GasLimit)
	}
	burned, berr := parseFloat64(b.Burned)
	if berr != nil || burned <= 0 {
		t.Fatalf("區塊 burned 應 >0，得到 %q", b.Burned)
	}
	t.Logf("block1 base_fee=%s gas_used=%d burned=%s", b.BaseFee, b.GasUsed, b.Burned)

	// 交易本身帶 burned（銷毀拆分）。
	txs, err := n.db.GetTransactionsByBlock(1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tx := range txs {
		if tx.TxHash == txHash {
			found = true
			if tx.MaxFee != "0.0001" || tx.PriorityFee != "0.000000001" || tx.GasLimit != 21000 {
				t.Fatalf("交易 EIP-1559 字段異常: %+v", tx)
			}
			if tx.Burned == "" {
				t.Fatal("交易缺少 burned")
			}
		}
	}
	if !found {
		t.Fatal("EIP-1559 交易未入塊")
	}

	// 全鏈審計：守恆含銷毀。
	audit, err := n.VerifyLedgerOnChain()
	if err != nil {
		t.Fatal(err)
	}
	if !audit.LedgerOK {
		t.Fatalf("審計守恆失敗: %+v", audit.Issues)
	}
	if audit.BurnedTotal <= 0 {
		t.Fatalf("審計 burned_total 應 >0，得到 %g", audit.BurnedTotal)
	}
	if math.Abs(audit.BalancesTotal+audit.BurnedTotal-audit.CoinbaseTotal) > 1e-6 {
		t.Fatalf("守恆不平衡: balances %g + burned %g != coinbase %g",
			audit.BalancesTotal, audit.BurnedTotal, audit.CoinbaseTotal)
	}
}

// TestEIP1559BaseFeeAdjust 驗證 base fee 動態調整：滿塊升、空塊降、有下限。
func TestEIP1559BaseFeeAdjust(t *testing.T) {
	// 滿塊（gasUsed=gasLimit）→ 上升。
	up := chaindb.ComputeNextBaseFee(0.000001, chaindb.BlockGasLimit, chaindb.BlockGasLimit)
	if up <= 0.000001 {
		t.Fatalf("滿塊應升 base fee，得到 %g", up)
	}
	// 空塊（gasUsed=0）→ 下降 12.5%，但不低於下限。
	down := chaindb.ComputeNextBaseFee(0.000001, 0, chaindb.BlockGasLimit)
	expected := 0.000001 * 7 / 8
	if math.Abs(down-expected) > 1e-12 {
		t.Fatalf("空塊應降 12.5%%（%g），得到 %g", expected, down)
	}
	// 下限保護。
	floor := chaindb.ComputeNextBaseFee(chaindb.MinBaseFee, 0, chaindb.BlockGasLimit)
	if floor < chaindb.MinBaseFee-1e-15 {
		t.Fatalf("base fee 不得低於下限，得到 %g", floor)
	}
	// 目標 50% 滿 → 不變。
	mid := chaindb.ComputeNextBaseFee(0.000001, chaindb.BlockGasLimit/2, chaindb.BlockGasLimit)
	if math.Abs(mid-0.000001) > 1e-12 {
		t.Fatalf("50%% 使用率應不變，得到 %g", mid)
	}
}

// TestEIP1559FeeSplit 驗證費用拆分：burn=base×gas、tip=剩餘。
func TestEIP1559FeeSplit(t *testing.T) {
	burn, tip := chaindb.SplitFee(0.05, 0.000001, 21000)
	if math.Abs(burn-0.021) > 1e-9 {
		t.Fatalf("burn 應為 0.021，得到 %g", burn)
	}
	if math.Abs(tip-0.029) > 1e-9 {
		t.Fatalf("tip 應為 0.029，得到 %g", tip)
	}
	// total 不足 burn → burn 截斷為 total、tip=0。
	b2, t2 := chaindb.SplitFee(0.01, 0.000001, 21000)
	if b2 != 0.01 || t2 != 0 {
		t.Fatalf("burn 截斷異常: %g %g", b2, t2)
	}
}

// TestEIP1559FeeTooLow 驗證：fee 低於 base×gas 被拒絕。
func TestEIP1559FeeTooLow(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, aliceKP)
	insertGenesisWithAlloc(t, n, alice)
	bobKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bob := mustAddr(t, bobKP)
	n.Start()
	defer n.Close()

	tx := signedTx(t, aliceKP, bob, "10", "0.0000001", 0) // 遠低於 base×gas
	tx["max_fee"] = "0.0001"
	tx["priority_fee"] = "0.000000001"
	tx["gas_limit"] = "21000"
	if _, err := n.SubmitTransaction(tx); err == nil {
		t.Fatal("fee 過低應被拒絕")
	}
}
