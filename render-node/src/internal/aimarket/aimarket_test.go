package aimarket

// aimarket_test.go — 套件級單元測試（不依賴 node / 錢包）。

import (
	"math"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func almostEq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestRegisterAndGetModel(t *testing.T) {
	s := newTestStore(t)
	m, err := s.RegisterModel("0xowner1", "gpt-tac", "a chat model", "cid-1", "", "1", 10, PriceTypeCall, 0)
	if err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	if m.Id == 0 || m.Version != "1" || m.Price != 10 || m.PriceType != PriceTypeCall {
		t.Fatalf("unexpected model: %+v", m)
	}
	got, err := s.GetModel(m.Id)
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if got.Name != "gpt-tac" || got.Owner != "0xowner1" {
		t.Fatalf("unexpected got: %+v", got)
	}
}

func TestRegisterValidation(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name                          string
		owner, mname, cid, url, v, pt string
		price                         float64
	}{
		{"empty owner", "", "m", "c", "", "1", PriceTypeCall, 1},
		{"empty name", "0xow", "", "c", "", "1", PriceTypeCall, 1},
		{"no location", "0xow", "m", "", "", "1", PriceTypeCall, 1},
		{"bad price type", "0xow", "m", "c", "", "1", "weekly", 1},
		{"negative price", "0xow", "m", "c", "", "1", PriceTypeCall, -1},
	}
	for _, c := range cases {
		if _, err := s.RegisterModel(c.owner, c.mname, "", c.cid, c.url, c.v, c.price, c.pt, 0); err == nil {
			t.Fatalf("%s: 應失敗", c.name)
		}
	}
}

func TestVersionBumpKeepsOld(t *testing.T) {
	s := newTestStore(t)
	m1, _ := s.RegisterModel("0xow", "vision", "v1", "cid-a", "", "1", 5, PriceTypeCall, 0)
	m2, _ := s.RegisterModel("0xow", "vision", "v2", "cid-b", "", "1", 6, PriceTypeCall, 0)
	if m1.Version != "1" || m2.Version != "2" {
		t.Fatalf("version bump: v1=%s v2=%s", m1.Version, m2.Version)
	}
	// 舊版仍在列表中。
	all, err := s.ListModels("", "")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("應保留舊版，got %d 筆", len(all))
	}
}

func TestListSearchAndSort(t *testing.T) {
	s := newTestStore(t)
	s.RegisterModel("0xow", "alpha-chat", "chat", "c", "", "", 1, PriceTypeCall, 0)
	mB, _ := s.RegisterModel("0xow", "beta-vision", "vision", "c", "", "", 2, PriceTypeCall, 0)
	// 給 beta 一次呼叫，使其 popular 排序領先。
	s.RecordInvocation("0xcaller", mB.Id, 2, "ok")

	byQ, err := s.ListModels("vision", "")
	if err != nil || len(byQ) != 1 || byQ[0].Name != "beta-vision" {
		t.Fatalf("search vision: %+v err=%v", byQ, err)
	}
	pop, _ := s.ListModels("", "popular")
	if len(pop) != 2 || pop[0].Id != mB.Id {
		t.Fatalf("popular sort: %+v", pop)
	}
}

func TestSubscribePerPeriod(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.RegisterModel("0xow", "big-model", "period", "c", "", "", 100, PriceTypePeriod, 0)
	if m.PeriodSec != DefaultPeriodSec {
		t.Fatalf("periodSec 預設應 %d, got %d", DefaultPeriodSec, m.PeriodSec)
	}
	sub, err := s.Subscribe("0xsub", m.Id)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if sub.ExpiresAt <= sub.CreatedAt || !sub.Active {
		t.Fatalf("bad sub: %+v", sub)
	}
	ok, err := s.HasActiveSubscription("0xsub", m.Id)
	if err != nil || !ok {
		t.Fatalf("HasActiveSubscription: ok=%v err=%v", ok, err)
	}
	// per_call 模型不可訂閱。
	mc, _ := s.RegisterModel("0xow", "cheap", "call", "c", "", "", 1, PriceTypeCall, 0)
	if _, err := s.Subscribe("0xsub", mc.Id); err == nil {
		t.Fatalf("per_call 模型訂閱應失敗")
	}
}

func TestInvokeCountAndQualify(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.RegisterModel("0xow", "chat", "c", "c", "", "", 1, PriceTypeCall, 0)
	inv, err := s.RecordInvocation("0xcaller", m.Id, 10, "ok")
	if err != nil {
		t.Fatalf("RecordInvocation: %v", err)
	}
	if inv.Id == 0 || inv.Fee != 10 {
		t.Fatalf("bad invocation: %+v", inv)
	}
	got, _ := s.GetModel(m.Id)
	if got.InvokeCount != 1 {
		t.Fatalf("InvokeCount 應 1, got %d", got.InvokeCount)
	}
	qual, _ := s.HasInvokedOrSubscribed("0xcaller", m.Id)
	if !qual {
		t.Fatalf("呼叫者應有評分資格")
	}
	// 不存在的模型 → 錯誤。
	if _, err := s.RecordInvocation("0xcaller", 9999, 1, "ok"); err == nil {
		t.Fatalf("不存在模型應失敗")
	}
}

func TestRateAverage(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.RegisterModel("0xow", "rated", "c", "c", "", "", 1, PriceTypeCall, 0)
	s.RecordInvocation("0xA", m.Id, 1, "ok")
	s.RecordInvocation("0xB", m.Id, 1, "ok")
	if _, err := s.RateModel(m.Id, "0xA", 5, "great"); err != nil {
		t.Fatalf("RateModel A: %v", err)
	}
	if _, err := s.RateModel(m.Id, "0xB", 3, "ok-ish"); err != nil {
		t.Fatalf("RateModel B: %v", err)
	}
	got, _ := s.GetModel(m.Id)
	if got.RatingCount != 2 || !almostEq(got.AvgRating, 4.0) {
		t.Fatalf("avg/count wrong: %+v", got)
	}
	// 重複評分＝更新（不增加 count）。
	s.RateModel(m.Id, "0xA", 5, "still great")
	got, _ = s.GetModel(m.Id)
	if got.RatingCount != 2 || !almostEq(got.AvgRating, 4.0) {
		t.Fatalf("upsert 後 avg/count wrong: %+v", got)
	}
	// 非法分數。
	if _, err := s.RateModel(m.Id, "0xA", 9, ""); err == nil {
		t.Fatalf("分數 9 應失敗")
	}
	ratings, _ := s.ListRatings(m.Id, 10)
	if len(ratings) != 2 {
		t.Fatalf("ListRatings 應 2 筆, got %d", len(ratings))
	}
}

func TestSplit(t *testing.T) {
	ownerPart, fee := Split(100)
	if !almostEq(fee, 10) || !almostEq(ownerPart, 90) {
		t.Fatalf("Split(100)=%v,%v", ownerPart, fee)
	}
	if ownerPart+fee != 100 {
		t.Fatalf("split 不平 %v+%v", ownerPart, fee)
	}
}
