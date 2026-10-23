package dapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "dapp-test-*")
	if err != nil {
		t.Fatalf("tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func validDeployer(t *testing.T) string {
	// 20-byte hex address（0x 前綴）。store 層只檢查非空，任意合法長度即可。
	return "0x" + strings.Repeat("a", 40)
}

func TestRegisterAndUpdate(t *testing.T) {
	s := testStore(t)
	addr := validDeployer(t)

	d, err := s.Register(addr, "MySwap", "DeFi", "desc", "https://example.com", "", "", "swap,defi", false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if d.Version != 1 || d.Name != "MySwap" {
		t.Fatalf("unexpected: %+v", d)
	}

	// 同名同 deployer → 更新且版本遞增。
	d2, err := s.Register(addr, "MySwap", "DeFi", "desc v2", "https://example.com", "", "", "swap", false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if d2.Id != d.Id {
		t.Fatalf("should reuse same id: %d vs %d", d.Id, d2.Id)
	}
	if d2.Version != 2 {
		t.Fatalf("version should increment: %d", d2.Version)
	}
	if d2.Description != "desc v2" {
		t.Fatalf("desc not updated: %q", d2.Description)
	}
}

func TestRegisterValidation(t *testing.T) {
	s := testStore(t)
	addr := validDeployer(t)
	cases := []struct{ name, cat, desc, url string }{
		{"", "DeFi", "d", "https://x.com"},                        // 缺名
		{"N", "DeFi", "d", "ftp://x.com"},                         // 非法 URL
		{"N", "DeFi", "d", "not-a-url"},                           // 非 URL
		{"N", "DeFi", strings.Repeat("x", 1001), "https://x.com"}, // 描述過長
	}
	for _, c := range cases {
		if _, err := s.Register(addr, c.name, c.cat, c.desc, c.url, "", "", "", false); err == nil {
			t.Fatalf("expected error for %+v", c)
		}
	}
	if _, err := s.Register("", "N", "DeFi", "d", "https://x.com", "", "", "", false); err == nil {
		t.Fatal("expected missing deployer error")
	}
}

func TestListSearchCategorySort(t *testing.T) {
	s := testStore(t)
	a1 := validDeployer(t)
	a2 := "0x" + strings.Repeat("b", 40)
	if _, err := s.Register(a1, "SwapDEX", "DeFi", "a dex", "https://a.com", "", "", "swap", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(a2, "GameNFT", "Game", "an nft game", "https://b.com", "", "", "nft", true); err != nil {
		t.Fatal(err)
	}

	// 關鍵字搜尋。
	r, err := s.List(ListParams{Q: "dex"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 || r.Items[0].Name != "SwapDEX" {
		t.Fatalf("search dex: %+v", r)
	}
	// 分類。
	r, _ = s.List(ListParams{Category: "Game"})
	if r.Total != 1 || r.Items[0].Category != "Game" {
		t.Fatalf("category Game: %+v", r)
	}
	// featured 排序：featured 先。
	r, _ = s.List(ListParams{Sort: "featured"})
	if r.Total != 2 || !r.Items[0].Featured {
		t.Fatalf("featured sort: %+v", r)
	}
	// deployer 過濾。
	r, _ = s.List(ListParams{Deployer: a2})
	if r.Total != 1 || r.Items[0].Deployer != a2 {
		t.Fatalf("deployer filter: %+v", r)
	}
}

func TestCategories(t *testing.T) {
	s := testStore(t)
	cats, err := s.Categories()
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) == 0 {
		t.Fatal("expected seeded categories")
	}
}

func TestRateNoDoubleCount(t *testing.T) {
	s := testStore(t)
	addr := validDeployer(t)
	d, _ := s.Register(addr, "R1", "DeFi", "d", "https://x.com", "", "", "", false)

	r, err := s.Rate(d.Id, addr, 5, "good")
	if err != nil {
		t.Fatal(err)
	}
	if r.Score != 5 {
		t.Fatalf("score: %+v", r)
	}
	got, _ := s.Get(d.Id)
	if got.RatingCount != 1 || got.RatingAvg != 5 {
		t.Fatalf("after first rate: %+v", got)
	}
	// 同 address 重複評分＝更新，不新增計數。
	if _, err := s.Rate(d.Id, addr, 3, "ok"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(d.Id)
	if got.RatingCount != 1 {
		t.Fatalf("rating count should stay 1: %+v", got)
	}
	if got.RatingAvg != 3 {
		t.Fatalf("avg should update to 3: %+v", got)
	}
	// 非法分數。
	if _, err := s.Rate(d.Id, addr, 9, ""); err == nil {
		t.Fatal("expected score range error")
	}
}

func TestFavoriteToggle(t *testing.T) {
	s := testStore(t)
	addr := validDeployer(t)
	d, _ := s.Register(addr, "F1", "DeFi", "d", "https://x.com", "", "", "", false)

	fav, got, err := s.FavoriteToggle(d.Id, addr)
	if err != nil {
		t.Fatal(err)
	}
	if !fav || got.FavCount != 1 {
		t.Fatalf("first toggle: fav=%v count=%d", fav, got.FavCount)
	}
	// 取消。
	fav, got, err = s.FavoriteToggle(d.Id, addr)
	if err != nil {
		t.Fatal(err)
	}
	if fav || got.FavCount != 0 {
		t.Fatalf("second toggle: fav=%v count=%d", fav, got.FavCount)
	}
	// 收藏列表。
	if _, _, err := s.FavoriteToggle(d.Id, addr); err != nil {
		t.Fatal(err)
	}
	items, err := s.Favorites(addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Id != d.Id {
		t.Fatalf("favorites: %+v", items)
	}
}

func TestAddView(t *testing.T) {
	s := testStore(t)
	addr := validDeployer(t)
	d, _ := s.Register(addr, "V1", "DeFi", "d", "https://x.com", "", "", "", false)
	_ = s.AddView(d.Id)
	_ = s.AddView(d.Id)
	got, _ := s.Get(d.Id)
	if got.Views != 2 {
		t.Fatalf("views: %d", got.Views)
	}
}

func TestOpenIdempotentSchema(t *testing.T) {
	dir, _ := os.MkdirTemp("", "dapp-reopen-*")
	defer func() { _ = os.RemoveAll(dir) }()
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()
	// 再次開啟不應因 schema 報錯（IF NOT EXISTS）。
	s2, err := Open(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("reopen parent dir: %v", err)
	}
	_ = s2.Close()
}
