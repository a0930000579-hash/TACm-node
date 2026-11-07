package node

// community_wallet_test.go — 社群統一權威地址（比照交易所 v160 錢包中心）。
//  1. 六個社群頁渲染（等同 GET 200）且引入權威地址解析腳本 community-wallet.js；
//  2. 模板不再以 /status（節點地址）作為用戶錢包地址來源；
//  3. 市場/社群 API 以「顯式 tx0 地址」呼叫時 happy path 正常。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/flosch/pongo2/v6"
)

// cmTplDir 從測試工作目錄（internal/node）定位模板目錄，並容錯幾個候選路徑。
func cmTplDir(t *testing.T) string {
	t.Helper()
	cands := []string{
		"../web/templates",          // go test 於 internal/node 下執行
		"internal/web/templates",    // go test 於 repo root 下執行
		"../internal/web/templates", // 其他相對配置
	}
	for _, c := range cands {
		if _, err := os.Stat(filepath.Join(c, "base.html")); err == nil {
			return c
		}
	}
	t.Fatalf("找不到模板目錄（candidate: %v）", cands)
	return ""
}

// cmStubContext 提供 base.html 渲染所需的最小 context。
func cmStubContext() pongo2.Context {
	return pongo2.Context{
		"status":    map[string]any{"Height": 10, "FinalizedHeight": 10},
		"node_only": false,
		"community": map[string]any{},
	}
}

// TestCommunityWalletPagesRender 六個社群頁渲染成功（200 等價）且含權威腳本引用。
func TestCommunityWalletPagesRender(t *testing.T) {
	dir := cmTplDir(t)
	tset := pongo2.NewSet("cm", pongo2.NewFSLoader(os.DirFS(dir)))
	pages := []string{
		"community.html", "friends.html", "live.html",
		"market.html", "profile.html", "videos.html",
	}
	for _, name := range pages {
		tpl, err := tset.FromFile(name)
		if err != nil {
			t.Fatalf("載入模板 %s: %v", name, err)
		}
		body, err := tpl.Execute(cmStubContext())
		if err != nil {
			t.Fatalf("渲染模板 %s（等同 GET 200）失敗: %v", name, err)
		}
		if !strings.Contains(body, "/static/js/community-wallet.js") {
			t.Errorf("%s 未引入權威地址解析腳本 community-wallet.js", name)
		}
		if !strings.Contains(body, "cmBindAddr") && !strings.Contains(body, "cmResolveAddr") {
			t.Errorf("%s 未使用權威地址解析（cmBindAddr/cmResolveAddr）", name)
		}
	}
}

// TestCommunityWalletNoStatusAddressSource market/profile 不得再以 /status 節點地址自動填入。
func TestCommunityWalletNoStatusAddressSource(t *testing.T) {
	dir := cmTplDir(t)
	for _, name := range []string{"market.html", "profile.html"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("讀取 %s: %v", name, err)
		}
		src := string(b)
		// 不得殘留「用 /status 回傳的節點地址自動當作用戶錢包」的自動綁定。
		if strings.Contains(src, "get('/status')") && strings.Contains(src, "st.address") {
			t.Errorf("%s 仍以 /status 的 st.address 自動填入用戶地址", name)
		}
		// 不得再把 cm_id 當成首要身分來源（僅 community-wallet.js 內部鏡像同步）。
		if strings.Contains(src, "localStorage.getItem('cm_id')") {
			t.Errorf("%s 仍直接讀取 cm_id 作為身分來源", name)
		}
	}
	// 共享腳本存在且公開兩個 API。
	jsPath := filepath.Join(dir, "..", "static", "js", "community-wallet.js")
	jb, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("讀取 community-wallet.js: %v", err)
	}
	js := string(jb)
	for _, want := range []string{"cmResolveAddr", "cmBindAddr", "tac_ex_wallet", "/api/auth/me"} {
		if !strings.Contains(js, want) {
			t.Errorf("community-wallet.js 缺少 %q", want)
		}
	}
}

// TestCommunityWalletAPIHappyPath 以顯式 tx0 地址呼叫市場/社群 API 應正常。
func TestCommunityWalletAPIHappyPath(t *testing.T) {
	_, ts := newCommunityHost(t, 0)
	alice := testAddr(t)
	bob := testAddr(t)

	// 發文（顯式 tx0 地址）。
	pid := cmPostJSON(t, ts.URL+"/api/community/post", map[string]any{
		"address": alice, "kind": "post", "content": "community wallet happy path",
	})
	id := int64(pid["post_id"].(float64))

	// 按讚。
	cmPostJSON(t, ts.URL+"/api/community/post/"+strconv.FormatInt(id, 10)+"/like", map[string]any{"address": bob})

	// 追蹤。
	cmPostJSON(t, ts.URL+"/api/community/follow", map[string]any{"follower": alice, "followee": bob})
	fw := cmGetJSON(t, ts.URL+"/api/community/following?address="+alice)
	if len(fw["items"].([]any)) != 1 {
		t.Fatalf("following 應 1 筆，得 %v", fw["items"])
	}

	// 市集上架（顯式 tx0 地址）。
	mk := cmPostJSON(t, ts.URL+"/api/community/market/create", map[string]any{
		"address": alice, "title": "test item", "body": "happy path",
		"price_tacm": 1.0,
	})
	mid := int64(mk["item_id"].(float64))

	// 買家購買意向（顯式 tx0 地址）。
	intent := cmGetJSON(t, ts.URL+"/api/community/market/"+strconv.FormatInt(mid, 10)+"/buy-intent?address="+bob)
	if intent["to"] != alice {
		t.Fatalf("buy-intent 收款方應為賣家 %s，得 %v", alice, intent["to"])
	}

	// 個人頁 meta（顯式 tx0 地址）。
	cmPostJSON(t, ts.URL+"/api/community/profile/meta", map[string]any{
		"address": alice, "display_name": "Alice", "avatar_url": "",
	})
	pm := cmGetJSON(t, ts.URL+"/api/community/profile/meta?address="+alice)
	if pm["display_name"] != "Alice" {
		t.Fatalf("profile meta 應存 display_name=Alice，得 %v", pm["display_name"])
	}
}
