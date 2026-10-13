package wallet

import (
	"math/big"
	"testing"
	"time"
)

// TestDistributeByHashrate 驗證算力瓜分：總和恆等於待分金額、占比符合算力比例。
func TestDistributeByHashrate(t *testing.T) {
	// 2:1 算力，分 8.8 TACM（8.8e18 wei）。
	amount, _ := new(big.Int).SetString("8800000000000000000", 10)
	splits := []MinerSplit{
		{Address: "m2", Share: big.NewInt(2_000_000)},
		{Address: "m1", Share: big.NewInt(1_000_000)},
	}
	out, err := DistributeByHashrate(amount, splits)
	if err != nil {
		t.Fatal(err)
	}
	sum := big.NewInt(0)
	for _, v := range out {
		sum.Add(sum, v)
	}
	if sum.Cmp(amount) != 0 {
		t.Fatalf("瓜分總和=%s 應等於 %s（守恆）", sum, amount)
	}
	// m2:m1 = 2:1 → 5.866e18 / 2.933e18（整除：2/3 與 1/3，末位補齊）。
	if out["m2"].Cmp(big.NewInt(5866666666666666666)) != 0 {
		t.Fatalf("m2 應得 5.8666e18, 實際=%s", out["m2"])
	}
	if out["m1"].Cmp(big.NewInt(2933333333333333334)) != 0 {
		t.Fatalf("m1 應得 2.9333e18, 實際=%s", out["m1"])
	}
	// 單礦工：全拿。
	one, err := DistributeByHashrate(amount, []MinerSplit{{Address: "solo", Share: big.NewInt(7)}})
	if err != nil || one["solo"].Cmp(amount) != 0 {
		t.Fatalf("單礦工應全拿: %v %s", err, one["solo"])
	}
	// 空集：nil。
	nilOut, err := DistributeByHashrate(amount, nil)
	if err != nil || nilOut != nil {
		t.Fatalf("空礦工集應回傳 nil: %v", err)
	}
}

// TestMinerStore 驗證礦機表 CRUD 與在線窗口判定。
func TestMinerStore(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RegisterMiner("m1", 1, 0); err != nil {
		t.Fatal(err)
	}
	// M36：分潤僅限「開機(active=1)」礦工——測試需先開機。
	if err := st.SetActiveMiner("m1", true); err != nil {
		t.Fatal(err)
	}
	if err := st.TickMiner("m1"); err != nil {
		t.Fatal(err)
	}
	// 未註冊心跳報錯。
	if err := st.TickMiner("ghost"); err == nil {
		t.Fatal("未註冊礦機心跳應報錯")
	}
	// M32：算力固定 1vCPU+2vGPU（忽略傳入）→ 5e6；推薦註冊提升（每人 +0.01CPU/+0.02GPU）。
	if err := st.RegisterMiner("m2", 99, 99); err != nil {
		t.Fatal(err)
	}
	// 推薦 3 人：m2 算力 = (1+0.03)×1e6 + (2+0.06)×2e6 = 1.03e6+4.12e6 = 5_150_000。
	if err := st.AddReferral("m2", "r1"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReferral("m2", "r2"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddReferral("m2", "r3"); err != nil {
		t.Fatal(err)
	}
	// 重複推薦不重複計。
	_ = st.AddReferral("m2", "r1")
	n, _ := st.ReferralCount("m2")
	if n != 3 {
		t.Fatalf("推薦數=%d want 3", n)
	}
	if err := st.RegisterMiner("m2", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveMiner("m2", true); err != nil {
		t.Fatal(err)
	}
	if err := st.TickMiner("m2"); err != nil {
		t.Fatal(err)
	}
	// 離線後不參與瓜分（active=0）。
	if err := st.StopMiner("m1"); err != nil {
		t.Fatal(err)
	}
	splits, err := st.OnlineMinerSplits(time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(splits) != 1 || splits[0].Address != "m2" || splits[0].Share.Cmp(big.NewInt(5_150_000)) != 0 {
		t.Fatalf("在線瓜分清單錯誤: %+v", splits)
	}
	ms, err := st.Miners()
	if err != nil || len(ms) != 2 {
		t.Fatalf("礦機清單錯誤: %+v %v", ms, err)
	}
	// online 判定：窗口內心跳為 true；停機後 false。
	for _, m := range ms {
		if m.Address == "m2" && !m.Online {
			t.Fatalf("m2 心跳新鮮應在線: %+v", m)
		}
	}
	if err := st.StopMiner("m2"); err != nil {
		t.Fatal(err)
	}
	ms2, _ := st.Miners()
	for _, m := range ms2 {
		if m.Address == "m2" && m.Online {
			t.Fatalf("m2 停機後不應在線: %+v", m)
		}
	}
}
