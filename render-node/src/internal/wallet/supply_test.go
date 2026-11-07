package wallet

import (
	"path/filepath"
	"testing"
)

func TestTiUSDSupplyFloorAndCaps(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "w"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	svc := NewService(st)

	// 未設定總供應 → 不啟用錨定（向後相容：可自由 mint/burn）。
	svc.SetTACMSupplyCap(0)
	if _, err := svc.MintTiUSD("a", 1000, "t"); err != nil {
		t.Fatalf("未錨定時 mint 應成功: %v", err)
	}

	// v102：市值錨定——先塞全鏈已出塊產出（mined＝總供應上限 52,003,300 TACm）。
	if _, err := st.db.Exec(`INSERT OR REPLACE INTO chain_stats (id, total_mined_tacm, updated_ts) VALUES (1, ?, strftime('%s'))`,
		"52003300000000000000000000"); err != nil {
		t.Fatalf("塞 mined: %v", err)
	}

	// 設定總供應 52,003,300 → 啟用錨定（未上市：cap＝floor＝mined×0.330）。
	svc.SetTACMSupplyCap(52003300)
	floor := int64(52003300) * 330000 // 52003300×0.33×1e6（micro）
	if floor != 17161089000000 {
		t.Fatalf("floor 計算錯誤: %d", floor)
	}
	_, cap0, en0, err := svc.TiUSDCap()
	if err != nil {
		t.Fatalf("TiUSDCap: %v", err)
	}
	if !en0 || cap0 != floor {
		t.Fatalf("未上市時 cap 應＝floor（0.330 倍）＝%d，實際 %d (en=%v)", floor, cap0, en0)
	}

	// 供給目前 1000 → 自動補鑄至 floor。
	top, err := svc.EnsureTiUSDFloor()
	if err != nil {
		t.Fatalf("EnsureTiUSDFloor: %v", err)
	}
	if top != floor-1000 {
		t.Fatalf("補鑄量應為 %d，實際 %d", floor-1000, top)
	}
	sup, _ := st.TiUSDSummary()
	if sup.Supply != floor {
		t.Fatalf("補鑄後供給應為 %d，實際 %d", floor, sup.Supply)
	}

	// 上市（市價 1.0 USDT/TACm）→ cap＝mined×1.0＝52,003,300×1e6（1:1 市值）。
	svc.SetTACMPrice(1.0)
	cap := int64(52003300) * 1000000

	// 上市後增發 100（供給 floor+100，未超 cap）再驗證銷毀下限。
	if _, err := svc.MintTiUSD("b", 100, "t"); err != nil {
		t.Fatalf("增發 100 應成功: %v", err)
	}
	// 銷毀 50（→ floor+50，仍 ≥ floor）→ 成功。
	if _, err := svc.BurnTiUSD(50, "t"); err != nil {
		t.Fatalf("高於下限銷毀應成功: %v", err)
	}
	// 銷毀 100（→ floor-50，低於 floor）→ 拒絕。
	if _, err := svc.BurnTiUSD(100, "t"); err == nil {
		t.Fatal("低於最小流通量銷毀應被拒絕")
	}
	sup, _ = st.TiUSDSummary()
	if _, err := svc.MintTiUSD("b", cap-sup.Supply, "t"); err != nil {
		t.Fatalf("補至上限應成功: %v", err)
	}
	if _, err := svc.MintTiUSD("b", 1, "t"); err == nil {
		t.Fatal("超過市值上限增發應被拒絕")
	}

	// v102：市值縮水 → 自動銷毀至新上限（1.0 → 0.1，cap 縮至 52,003,300×1e5）。
	svc.SetTACMPrice(0.1)
	newCap := int64(52003300) * 100000
	burned, err := svc.EnsureTiUSDCap()
	if err != nil {
		t.Fatalf("EnsureTiUSDCap: %v", err)
	}
	if burned != cap-newCap {
		t.Fatalf("市值縮水應銷毀 %d，實際 %d", cap-newCap, burned)
	}
	sup, _ = st.TiUSDSummary()
	if sup.Supply != newCap {
		t.Fatalf("銷毀後供給應為 %d，實際 %d", newCap, sup.Supply)
	}
	// 銷毀後供給應仍 ≥ floor（floor＝min(cap, mined×0.330)）。
	fl, _, _, _ := svc.TiUSDCap()
	if fl > sup.Supply {
		t.Fatalf("floor(%d) 不應大於供給(%d)", fl, sup.Supply)
	}
}
