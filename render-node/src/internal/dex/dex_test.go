package dex

import (
	"errors"
	"math/big"
	"path/filepath"
	"testing"
)

// fakeLedger 記憶體帳本：模擬 VM 標準代幣餘額（無需真實合約即可測 AMM 數學）。
type fakeLedger struct {
	bal map[string]map[string]*big.Int // holder -> contract -> amount
}

func newFakeLedger(contracts []string, holders map[string]map[string]int64) *fakeLedger {
	l := &fakeLedger{bal: map[string]map[string]*big.Int{}}
	for h, cs := range holders {
		l.bal[h] = map[string]*big.Int{}
		for c, a := range cs {
			l.bal[h][c] = big.NewInt(a)
		}
	}
	return l
}

func (l *fakeLedger) transfer(from, to, contract string, amount *big.Int) error {
	if l.bal[from] == nil || l.bal[from][contract] == nil ||
		l.bal[from][contract].Cmp(amount) < 0 {
		return ErrInsufficient
	}
	l.bal[from][contract] = new(big.Int).Sub(l.bal[from][contract], amount)
	if l.bal[to] == nil {
		l.bal[to] = map[string]*big.Int{}
	}
	if l.bal[to][contract] == nil {
		l.bal[to][contract] = big.NewInt(0)
	}
	l.bal[to][contract] = new(big.Int).Add(l.bal[to][contract], amount)
	return nil
}

func (l *fakeLedger) balance(holder, contract string) (*big.Int, error) {
	if l.bal[holder] == nil || l.bal[holder][contract] == nil {
		return big.NewInt(0), nil
	}
	return new(big.Int).Set(l.bal[holder][contract]), nil
}

func setup(t *testing.T) (*Engine, *fakeLedger, string, string, string) {
	t.Helper()
	ledger := newFakeLedger(nil, map[string]map[string]int64{
		"admin": {"TKA": 1000000, "TKB": 1000000},
	})
	e, err := New(t.TempDir(), "admin",
		func(poolID, from, to, contract string, amount *big.Int) error {
			return ledger.transfer(from, to, contract, amount)
		},
		func(holder, contract string) (*big.Int, error) {
			return ledger.balance(holder, contract)
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	_ = filepath.Join
	return e, ledger, "admin", "TKA", "TKB"
}

func TestCreatePool(t *testing.T) {
	e, _, _, a, b := setup(t)
	p, err := e.CreatePool(a, b, "pool1addr")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "p1" || p.Token0 != a || p.Token1 != b || p.PoolAddr != "pool1addr" {
		t.Fatalf("池欄位異常: %+v", p)
	}
	// 重複交易對（順序相反）拒絕。
	if _, err := e.CreatePool(b, a, "pool2addr"); err == nil {
		t.Fatal("重複交易對應被拒絕")
	}
	// 同幣拒絕。
	if _, err := e.CreatePool(a, a, "pool3addr"); err == nil {
		t.Fatal("同幣池應被拒絕")
	}
}

func TestAddAndSwapConstantProduct(t *testing.T) {
	e, _, admin, a, b := setup(t)
	if _, err := e.CreatePool(a, b, "pool1addr"); err != nil {
		t.Fatal(err)
	}
	// 注入 1000/500（首次：LP = sqrt(500000)）。
	p, err := e.AddLiquidity("p1", admin, big.NewInt(1000), big.NewInt(500))
	if err != nil {
		t.Fatal(err)
	}
	if p.Reserve0.Cmp(big.NewInt(1000)) != 0 || p.Reserve1.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("儲備異常: %s/%s", p.Reserve0, p.Reserve1)
	}
	if p.LPTotal.String() != "707" { // sqrt(500000)≈707
		t.Fatalf("LP 份額應為 707，得到 %s", p.LPTotal)
	}
	pos, err := e.PositionOf("p1", admin)
	if err != nil || pos.Shares.String() != "707" {
		t.Fatalf("持倉異常: %v %v", pos, err)
	}

	// Quote：1 TKA 因 0.3% 手續費整數截斷（in'=0）→ 應報流動性不足。
	if _, _, err := e.Quote("p1", a, big.NewInt(1)); err == nil {
		t.Fatal("1 TKA 應因手續費截斷而不可兌換")
	}
	// Quote 100 TKA（in'=99.7→99）：out = 500 - 500000/1099 ≈ 46。
	tOut, out, err := e.Quote("p1", a, big.NewInt(100))
	if err != nil {
		t.Fatal(err)
	}
	if tOut != b {
		t.Fatal("輸出代幣錯誤")
	}
	if out.Cmp(big.NewInt(46)) != 0 {
		t.Fatalf("Quote(100 TKA) 應為 46 TKB，得到 %s", out)
	}

	// Swap 100 TKA → 池轉出 46 TKB；admin 餘額變動。
	tOut, out, err = e.Swap("p1", admin, a, big.NewInt(100), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tOut != b || out.Cmp(big.NewInt(46)) != 0 {
		t.Fatalf("Swap 異常: out=%s token=%s", out, tOut)
	}
	bal, _ := e.balance(admin, b)
	if bal.Cmp(big.NewInt(999546)) != 0 { // 1,000,000 - 500(注入) + 46(swap 收)
		t.Fatalf("admin TKB 應為 999,546，得到 %s", bal)
	}
	p, _ = e.Get("p1")
	if p.Reserve0.Cmp(big.NewInt(1100)) != 0 || p.Reserve1.Cmp(big.NewInt(454)) != 0 {
		t.Fatalf("Swap 後儲備異常: %s/%s", p.Reserve0, p.Reserve1)
	}
}

func TestRemoveLiquidity(t *testing.T) {
	e, _, admin, a, b := setup(t)
	if _, err := e.CreatePool(a, b, "pool1addr"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddLiquidity("p1", admin, big.NewInt(1000), big.NewInt(500)); err != nil {
		t.Fatal(err)
	}
	// 退出 50% 份額（707/2=353 取整）。
	p, err := e.RemoveLiquidity("p1", admin, big.NewInt(353))
	if err != nil {
		t.Fatal(err)
	}
	if p.Reserve0.Cmp(big.NewInt(1000)) == 0 {
		t.Fatalf("退出後儲備應減少: %s", p.Reserve0)
	}
	// admin 應拿回約一半（500 TKA、250 TKB；整數除法 1000*353/707=499）。
	// admin 原有 1,000,000，注入後 999,000/999,500；退出後 999,499/999,749。
	balA, _ := e.balance(admin, a)
	balB, _ := e.balance(admin, b)
	if balA.Cmp(big.NewInt(999499)) != 0 {
		t.Fatalf("退出後 admin TKA 應為 999,499，得到 %s", balA)
	}
	if balB.Cmp(big.NewInt(999749)) != 0 { // 999,500 + 249
		t.Fatalf("退出後 admin TKB 應為 999,749，得到 %s", balB)
	}
	pos, _ := e.PositionOf("p1", admin)
	if pos.Shares.Cmp(big.NewInt(354)) != 0 {
		t.Fatalf("剩餘份額應為 354，得到 %s", pos.Shares)
	}
}

func TestErrors(t *testing.T) {
	e, _, admin, a, b := setup(t)
	if _, err := e.CreatePool(a, b, "pool1addr"); err != nil {
		t.Fatal(err)
	}
	// 未知池。
	if _, err := e.AddLiquidity("p9", admin, big.NewInt(1), big.NewInt(1)); err == nil {
		t.Fatal("未知池應報錯")
	}
	// 未知代幣 swap。
	if _, _, err := e.Swap("p1", admin, "TKC", big.NewInt(1), nil); err == nil {
		t.Fatal("未知代幣應報錯")
	}
	// 零金額。
	if _, _, err := e.Quote("p1", a, big.NewInt(0)); err == nil {
		t.Fatal("零金額應報錯")
	}
	// 未注入前 swap。
	if _, _, err := e.Swap("p1", admin, a, big.NewInt(1), nil); err == nil {
		t.Fatal("無流動性時 swap 應報錯")
	}
	// 超額退出。
	if _, err := e.RemoveLiquidity("p1", admin, big.NewInt(1)); err == nil {
		t.Fatal("無持倉退出應報錯")
	}
}

// TestSwapSlippage：滑點保護——minOut 過高拒絕，可接受則成交。
func TestSwapSlippage(t *testing.T) {
	e, _, admin, a, b := setup(t)
	pool, err := e.CreatePool(a, b, "poolA")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddLiquidity(pool.ID, admin, big.NewInt(800000), big.NewInt(400000)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.Swap(pool.ID, admin, a, big.NewInt(1000), big.NewInt(999999999)); !errors.Is(err, ErrSlippage) {
		t.Fatalf("want ErrSlippage, got %v", err)
	}
	_, out, err := e.Swap(pool.ID, admin, a, big.NewInt(1000), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Sign() <= 0 {
		t.Fatal("out must be positive")
	}
	minOK := new(big.Int).Mul(out, big.NewInt(99))
	minOK.Div(minOK, big.NewInt(100))
	_, out2, err := e.Swap(pool.ID, admin, a, big.NewInt(1000), minOK)
	if err != nil {
		t.Fatalf("swap with acceptable minOut should succeed: %v", err)
	}
	if out2.Cmp(minOK) < 0 {
		t.Fatalf("out below minOut: %s < %s", out2, minOK)
	}
}
