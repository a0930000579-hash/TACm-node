package node

// v163_dapp_e2e_test.go — DApp 平台端到端冒煙：
// 註冊 → list 含該筆 → detail → 評分（重複不重複計數）→ favorite toggle →
// favorites/mine 正確；非法地址與缺欄位回 4xx 錯誤。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// dappPost 發 POST 並回 (status, body)。
func dappPost(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestV163DAppE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	defer srv.Close()

	if n.dappSt == nil {
		t.Fatal("dappSt 未啟用")
	}

	addr0x, _ := aimTestAddr(t)
	badAddr := "0xnotanaddress"

	// 1) 非法地址註冊 → 4xx。
	code, out := dappPost(t, srv.URL+"/api/dapp/register", map[string]any{
		"address": badAddr, "name": "X", "url": "https://x.com",
	})
	if code < 400 || code > 499 {
		t.Fatalf("bad addr expected 4xx, got %d: %v", code, out)
	}

	// 2) 缺 URL → 4xx（store 層校驗）。
	code, out = dappPost(t, srv.URL+"/api/dapp/register", map[string]any{
		"address": addr0x, "name": "NoURL", "url": "ftp://bad",
	})
	if code < 400 {
		t.Fatalf("bad url expected 4xx, got %d: %v", code, out)
	}

	// 3) 正常註冊。
	code, out = dappPost(t, srv.URL+"/api/dapp/register", map[string]any{
		"address": addr0x, "name": "TACSwap", "category": "DeFi",
		"description": "onchain dex", "url": "https://tacswap.example", "tags": "swap,defi",
	})
	if code != 200 || !out["ok"].(bool) {
		t.Fatalf("register failed: %d %v", code, out)
	}
	dapp := out["dapp"].(map[string]any)
	id := dapp["id"].(float64)
	if dapp["version"].(float64) != 1 {
		t.Fatalf("version: %v", dapp["version"])
	}

	// 同名同 deployer 再註冊 → version=2。
	_, out = dappPost(t, srv.URL+"/api/dapp/register", map[string]any{
		"address": addr0x, "name": "TACSwap", "description": "v2", "url": "https://tacswap.example",
	})
	if !out["ok"].(bool) || out["dapp"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("update version: %v", out)
	}

	// 4) list 含該 DApp。
	lst := getJSON(t, srv.URL+"/api/dapp/list?sort=new&size=50")
	if !lst["ok"].(bool) {
		t.Fatalf("list: %v", lst)
	}
	res := lst["result"].(map[string]any)
	if res["total"].(float64) < 1 {
		t.Fatalf("list total: %v", res)
	}

	// 5) detail。
	det := getJSON(t, srv.URL+"/api/dapp/detail?id="+strconv.FormatFloat(id, 'f', 0, 64))
	if !det["ok"].(bool) {
		t.Fatalf("detail: %v", det)
	}

	// 6) 評分 5 → rating_count=1 avg=5。
	code, out = dappPost(t, srv.URL+"/api/dapp/rate", map[string]any{
		"address": addr0x, "dapp_id": int64(id), "score": 5,
	})
	if code != 200 || !out["ok"].(bool) {
		t.Fatalf("rate: %d %v", code, out)
	}
	got := out["dapp"].(map[string]any)
	if got["rating_count"].(float64) != 1 || got["rating_avg"].(float64) != 5 {
		t.Fatalf("after rate: %v", got)
	}
	// 重複評分 3 → count 仍 1，avg=3。
	code, out = dappPost(t, srv.URL+"/api/dapp/rate", map[string]any{
		"address": addr0x, "dapp_id": int64(id), "score": 3,
	})
	got = out["dapp"].(map[string]any)
	if got["rating_count"].(float64) != 1 || got["rating_avg"].(float64) != 3 {
		t.Fatalf("after re-rate: %v", got)
	}
	// 非法分數 → 4xx。
	code, _ = dappPost(t, srv.URL+"/api/dapp/rate", map[string]any{
		"address": addr0x, "dapp_id": int64(id), "score": 9,
	})
	if code < 400 {
		t.Fatalf("bad score expected 4xx, got %d", code)
	}

	// 7) favorite toggle。
	code, out = dappPost(t, srv.URL+"/api/dapp/favorite", map[string]any{
		"address": addr0x, "dapp_id": int64(id),
	})
	if code != 200 || out["favorited"].(bool) != true {
		t.Fatalf("favorite on: %d %v", code, out)
	}
	// favorites 含該筆。
	favs := getJSON(t, srv.URL+"/api/dapp/favorites?address="+addr0x)
	items := favs["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("favorites: %v", favs)
	}
	// 取消收藏。
	_, out = dappPost(t, srv.URL+"/api/dapp/favorite", map[string]any{
		"address": addr0x, "dapp_id": int64(id),
	})
	if out["favorited"].(bool) != false {
		t.Fatalf("favorite off: %v", out)
	}

	// 8) mine（依 deployer）。
	mine := getJSON(t, srv.URL+"/api/dapp/mine?deployer="+addr0x)
	mres := mine["result"].(map[string]any)
	if mres["total"].(float64) < 1 {
		t.Fatalf("mine: %v", mine)
	}

	// 9) categories。
	cats := getJSON(t, srv.URL+"/api/dapp/categories")
	if len(cats["categories"].([]any)) == 0 {
		t.Fatalf("categories empty: %v", cats)
	}
}
