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

	// 設定總供應 52,003,300 → 啟用錨定。
	svc.SetTACMSupplyCap(52003300)
	floor := int64(52003300) * 330000 // 52003300×0.33×1e6（micro）
	if floor != 17161089000000 {
		t.Fatalf("floor 計算錯誤: %d", floor)
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

	// 先增發 100（供給 floor+100，未超上限）再驗證銷毀下限。
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

	// 增發至超上限 → 拒絕：上限＝52,003,300×1e6。
	cap := int64(52003300) * 1000000
	sup, _ = st.TiUSDSummary()
	if _, err := svc.MintTiUSD("b", cap-sup.Supply, "t"); err != nil {
		t.Fatalf("補至上限應成功: %v", err)
	}
	if _, err := svc.MintTiUSD("b", 1, "t"); err == nil {
		t.Fatal("超過總供應上限增發應被拒絕")
	}
}
