package chaindb

// M156 委託重放單元測試：委託/撤銷/權重/超額防護/解析。

import (
	"math"
	"path/filepath"
	"strconv"
	"testing"
)

func openTestDB(t *testing.T) *ChainDB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("開庫失敗: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestDelegationReplay(t *testing.T) {
	db := openTestDB(t)
	del, val := "tx0delegator000000000000000000000000000", "tx0validator00000000000000000000000000"

	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyDelegationTx(tx, del, val, 1000, 1); err != nil {
		t.Fatalf("委託失敗: %v", err)
	}
	if err := applyDelegationTx(tx, del, val, 500, 2); err != nil {
		t.Fatalf("再委託失敗: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	total, err := db.GetDelegationTotal(del, val)
	if err != nil || total != 1500 {
		t.Fatalf("委託額應 1500，got %v err %v", total, err)
	}
	// 驗證人權重合併（自有質押 + 委託）。
	vs, err := db.GetPosValidators()
	if err != nil || len(vs) != 1 || vs[0].TotalStake != 1500 {
		t.Fatalf("驗證人權重應 1500，got %+v err %v", vs, err)
	}

	// 撤銷 400。
	tx2, _ := db.db.Begin()
	if err := applyUndelegationTx(tx2, del, val, 400, 3, 3); err != nil {
		t.Fatalf("撤銷失敗: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	total, _ = db.GetDelegationTotal(del, val)
	if total != 1100 {
		t.Fatalf("撤銷後應 1100，got %v", total)
	}

	// 超額撤銷 → 錯誤（不扣成負數）。
	tx3, _ := db.db.Begin()
	if err := applyUndelegationTx(tx3, del, val, 9999, 4, 4); err == nil {
		t.Fatal("超額撤銷應報錯")
	}
	_ = tx3.Rollback()
	total, _ = db.GetDelegationTotal(del, val)
	if total != 1100 {
		t.Fatalf("超額撤銷不應扣減，got %v", total)
	}
}

func TestParseDelegationMemo(t *testing.T) {
	cases := []struct {
		memo string
		del  string
		val  string
		ok   bool
	}{
		{"pos:delegate:tx0a:tx0b", "tx0a", "tx0b", true},
		{"pos:undelegate:tx0a:tx0b", "tx0a", "tx0b", true},
		{"pos:stake", "", "", false},
		{"pos:delegate:tx0a", "", "", false},
		{"vm:call:00", "", "", false},
	}
	for _, c := range cases {
		del, val, ok := ParseDelegationMemo(c.memo)
		if ok != c.ok || (ok && (del != c.del || val != c.val)) {
			t.Fatalf("%s → (%s,%s,%v)，want (%s,%s,%v)", c.memo, del, val, ok, c.del, c.val, c.ok)
		}
	}
}

func TestGetDelegationsAndCount(t *testing.T) {
	db := openTestDB(t)
	del, val := "tx0delegator000000000000000000000000000", "tx0validator00000000000000000000000000"
	del2, val2 := "tx0delegator200000000000000000000000000", "tx0validator20000000000000000000000000"
	tx, _ := db.db.Begin()
	if err := applyDelegationTx(tx, del, val, 100, 1); err != nil {
		t.Fatal(err)
	}
	if err := applyDelegationTx(tx, del, val2, 200, 2); err != nil {
		t.Fatal(err)
	}
	if err := applyDelegationTx(tx, del2, val2, 300, 3); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dels, err := db.GetDelegations(del)
	if err != nil || len(dels) != 2 {
		t.Fatalf("del 應 2 筆委託，got %+v err %v", dels, err)
	}
	vd, err := db.GetValidatorDelegations(val2)
	if err != nil || len(vd) != 2 {
		t.Fatalf("val2 應 2 筆委託，got %+v err %v", vd, err)
	}
	n, err := db.CountDelegations()
	if err != nil || n != 3 {
		t.Fatalf("委託關係應 3，got %v err %v", n, err)
	}
}

func TestCoinbaseDelegRewardSplit(t *testing.T) {
	// 恆等式：有委託時 node＋deleg 合計＝無委託時的 node 份額。
	reward := 10.0 // NodeShareBps=4500 → nodeShare=4.5
	h, ts := int64(42), int64(1000)
	prop := "tx0proposer0000000000000000000000000000"
	del := "tx0delegator000000000000000000000000000"
	val := "tx0validator00000000000000000000000000"

	noDeleg := BuildCoinbaseTxs(h, prop, ts, nil, reward, val)
	var noDelegNode float64
	for _, tx := range noDeleg {
		if tx.Memo == "coinbase:node" {
			v, _ := strconv.ParseFloat(tx.Amount, 64)
			noDelegNode += v
		}
	}
	if noDelegNode <= 0 {
		t.Fatal("無委託節點份額應 >0")
	}

	// 委託 1000、驗證人權重 2000（自有 1000）→ 委託占比 0.5。
	db := openTestDB(t)
	tx, _ := db.db.Begin()
	if err := applyDelegationTx(tx, del, val, 1000, ts); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 驗證人權重 = 自有質押 1000 + 委託 1000 = 2000（模擬 pos:stake 已入帳）。
	tx2, _ := db.db.Begin()
	if err := applyDelegationTx(tx2, "tx0selfstake000000000000000000000000000", val, 1000, ts); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}

	dels, err := db.GetValidatorDelegations(val)
	if err != nil || len(dels) != 2 {
		t.Fatalf("委託應 2 筆，got %+v err %v", dels, err)
	}
	vs, _ := db.GetPosValidators()
	stake := 0.0
	for _, v := range vs {
		if v.Address == val {
			stake = v.TotalStake
		}
	}
	if stake != 2000 {
		t.Fatalf("權重應 2000，got %v", stake)
	}
	nodeShare := reward * 4500 / 10000 // 4.5
	per := nodeShare / 1
	totalDeleg := 1000.0
	// 節點層演算法（與 node.go 相同）：
	var list []DelegationReward
	acc := 0.0
	for i, d := range dels {
		var amt float64
		if i == len(dels)-1 {
			amt = per*totalDeleg/stake - acc
		} else {
			amt = per * d.Amount / stake
			acc += amt
		}
		if amt > 1e-9 {
			list = append(list, DelegationReward{Delegator: d.Delegator, Amount: amt})
		}
	}
	withDeleg := BuildCoinbaseTxsWithDelegRewards(h, prop, ts, nil, reward, map[string][]DelegationReward{val: list}, val)
	var nodeGot, delegGot float64
	for _, tx := range withDeleg {
		v, _ := strconv.ParseFloat(tx.Amount, 64)
		switch tx.Memo {
		case "coinbase:node":
			nodeGot += v
		case "coinbase:deleg":
			delegGot += v
		}
	}
	if math.Abs(nodeGot+delegGot-noDelegNode) > 1e-6 {
		t.Fatalf("恆等式破：node+deleg=%v 應 =%v", nodeGot+delegGot, noDelegNode)
	}
	// 委託占比 0.5 → 委託者合計約 0.5*per。
	if math.Abs(delegGot-per*0.5) > 1e-6 {
		t.Fatalf("委託獎勵應 ≈%v，got %v", per*0.5, delegGot)
	}
}

func TestUnbondingFlow(t *testing.T) {
	db := openTestDB(t)
	del := "tx0delegator000000000000000000000000000"
	val := "tx0validator00000000000000000000000000"

	// 委託 500。
	tx, _ := db.db.Begin()
	if err := applyDelegationTx(tx, del, val, 500, 1); err != nil {
		t.Fatal(err)
	}
	// 質押池鎖倉入帳（鏈上帳戶，unbond_pay 扣款來源）。
	if _, err := tx.Exec(`INSERT OR REPLACE INTO accounts (address, balance, nonce) VALUES (?, ?, 0)`, PosStakePoolAddr, 500); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// 高度 10 撤銷 500 → 進入解綁佇列（complete = 10+100 = 110）。
	tx2, _ := db.db.Begin()
	if err := applyUndelegationTx(tx2, del, val, 500, 10, 10); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	ubs, err := db.GetUnbondings(del)
	if err != nil || len(ubs) != 1 || ubs[0].CompleteHeight != 110 || ubs[0].Status != "pending" {
		t.Fatalf("解綁佇列應 1 筆 pending（complete 110），got %+v err %v", ubs, err)
	}

	// 高度 50 未到期 → 不返還。
	due, err := db.ListPendingUnbondings(50)
	if err != nil || len(due) != 0 {
		t.Fatalf("未到期應無返還，got %+v err %v", due, err)
	}

	// 高度 110 到期 → applyUnbondPay 返還並標 paid。
	tx3, _ := db.db.Begin()
	if err := applyUnbondPay(tx3, del, val, 500, 110); err != nil {
		t.Fatal(err)
	}
	if err := tx3.Commit(); err != nil {
		t.Fatal(err)
	}
	ubs, _ = db.GetUnbondings(del)
	if len(ubs) != 1 || ubs[0].Status != "paid" {
		t.Fatalf("解綁應標 paid，got %+v", ubs)
	}
	// 質押池扣減完成。
	var bal float64
	_ = db.db.QueryRow(`SELECT balance FROM accounts WHERE address = ?`, PosStakePoolAddr).Scan(&bal)
	if bal != 0 {
		t.Fatalf("質押池應歸零，got %v", bal)
	}
	// 重放重複 pay → 錯誤（防偽造/雙花）。
	tx4, _ := db.db.Begin()
	if err := applyUnbondPay(tx4, del, val, 500, 111); err == nil {
		t.Fatal("重複返還應報錯")
	}
	_ = tx4.Rollback()
}
