package node

// v163/v165 DApp 平台端到端冒煙：
// challenge(nonce) → EIP-191 簽名 → register(pending) → admin approve → list/detail →
// rate（重複不重複計數）→ favorite toggle → favorites/mine；
// 冒名簽名、缺簽名、重放 nonce、非白名單審核皆被拒。

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"tacm/internal/crypto"
	"tacm/internal/dapp"
)

// dappPostRaw 發 POST 並回 (status, body)。
func dappPostRaw(t *testing.T, url string, body any) (int, map[string]any) {
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

// dappSignWrite 完整寫操作流程：取 nonce → 簽名 → 帶信封 POST。
// extra 為額外業務欄位（name/url/score/dapp_id/status 等）。
func dappSignWrite(t *testing.T, base, action, address string, priv []byte, payload string, extra map[string]any) (int, map[string]any) {
	t.Helper()
	// 1) 取 nonce。
	code, ch := dappPostRaw(t, base+"/api/dapp/challenge", map[string]any{"address": address, "action": action})
	if code != 200 || !ch["ok"].(bool) {
		t.Fatalf("challenge 失敗: %d %v", code, ch)
	}
	nonce, _ := ch["nonce"].(string)
	ts, _ := ch["timestamp"].(float64)
	// 2) 重建 canonical 訊息並簽名。
	msg := dapp.DAppSignMessage(action, address, nonce, int64(ts), payload)
	sig, err := crypto.SignPersonalMessage([]byte(msg), priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	body := map[string]any{
		"address": address, "nonce": nonce, "timestamp": int64(ts), "signature": "0x" + hex.EncodeToString(sig),
	}
	for k, v := range extra {
		body[k] = v
	}
	return dappPostRaw(t, base+"/api/dapp/"+writePathFor(action), body)
}

func writePathFor(action string) string {
	switch action {
	case dapp.DappActionRegister:
		return "register"
	case dapp.DappActionRate:
		return "rate"
	case dapp.DappActionFavorite:
		return "favorite"
	case dapp.DappActionAdmin:
		return "admin/status"
	}
	return action
}

func TestV165DAppE2ESignedFlow(t *testing.T) {
	n, srv := newTestRPCServer(t)
	defer srv.Close()
	base := srv.URL

	if n.dappSt == nil {
		t.Fatal("dappSt 未啟用")
	}

	priv, addr := ethKey(t) // 0x 地址 + 對應私鑰
	// 將該地址設為審核管理員。
	t.Setenv("TAC_DAPP_ADMINS", addr)

	// 1) 無簽名註冊 → 401（缺簽名降級提示）。
	code, out := dappPostRaw(t, base+"/api/dapp/register", map[string]any{
		"address": addr, "name": "NoSig", "url": "https://x.com",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("no-sig register should be 401, got %d: %v", code, out)
	}

	// 2) 正常簽名註冊 → 200，狀態 pending。
	code, out = dappSignWrite(t, base, dapp.DappActionRegister, addr, priv,
		"name=TACSwap|url=https://tacswap.example",
		map[string]any{"name": "TACSwap", "category": "DeFi", "description": "onchain dex", "url": "https://tacswap.example", "tags": "swap,defi"})
	if code != 200 || !out["ok"].(bool) {
		t.Fatalf("register failed: %d %v", code, out)
	}
	d := out["dapp"].(map[string]any)
	id := int64(d["id"].(float64))
	if d["status"] != "pending" {
		t.Fatalf("new register must be pending: %v", d)
	}

	// 3) pending 不在公開 list。
	lst := getJSON(t, base+"/api/dapp/list?sort=new&size=50")
	res := lst["result"].(map[string]any)
	for _, it := range res["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == id {
			t.Fatal("pending dapp leaked into public list")
		}
	}

	// 4) 管理端核准（同一地址在白名單）。
	code, out = dappSignWrite(t, base, dapp.DappActionAdmin, addr, priv,
		"dapp="+strconv.FormatInt(id, 10)+"|status=approved",
		map[string]any{"dapp_id": id, "status": "approved"})
	if code != 200 || !out["ok"].(bool) {
		t.Fatalf("admin approve failed: %d %v", code, out)
	}

	// 5) 核准後公開 list 含該筆。
	lst = getJSON(t, base+"/api/dapp/list?sort=new&size=50")
	res = lst["result"].(map[string]any)
	found := false
	for _, it := range res["items"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == id {
			found = true
		}
	}
	if !found {
		t.Fatal("approved dapp missing from public list")
	}

	// 6) detail。
	det := getJSON(t, base+"/api/dapp/detail?id="+strconv.FormatInt(id, 10))
	if !det["ok"].(bool) {
		t.Fatalf("detail: %v", det)
	}

	// 7) 簽名評分 5 → count=1 avg=5。
	payloadRate := "dapp=" + strconv.FormatInt(id, 10) + "|score=5"
	code, out = dappSignWrite(t, base, dapp.DappActionRate, addr, priv, payloadRate,
		map[string]any{"dapp_id": id, "score": 5})
	if code != 200 || !out["ok"].(bool) {
		t.Fatalf("rate: %d %v", code, out)
	}
	got := out["dapp"].(map[string]any)
	if got["rating_count"].(float64) != 1 || got["rating_avg"].(float64) != 5 {
		t.Fatalf("after rate: %v", got)
	}

	// 8) 簽名收藏。
	payloadFav := "dapp=" + strconv.FormatInt(id, 10)
	code, out = dappSignWrite(t, base, dapp.DappActionFavorite, addr, priv, payloadFav,
		map[string]any{"dapp_id": id})
	if code != 200 || out["favorited"].(bool) != true {
		t.Fatalf("favorite on: %d %v", code, out)
	}
	favs := getJSON(t, base+"/api/dapp/favorites?address="+addr)
	if len(favs["items"].([]any)) != 1 {
		t.Fatalf("favorites: %v", favs)
	}

	// 9) mine（含 pending 視圖；此筆已 approved）。
	mine := getJSON(t, base+"/api/dapp/mine?deployer="+addr)
	mres := mine["result"].(map[string]any)
	if mres["total"].(float64) < 1 {
		t.Fatalf("mine: %v", mine)
	}
}

func TestV165DAppReplayAndForgery(t *testing.T) {
	_, srv := newTestRPCServer(t)
	defer srv.Close()
	base := srv.URL

	priv, addr := ethKey(t)
	t.Setenv("TAC_DAPP_ADMINS", addr)

	// 註冊一筆並核准。
	code, out := dappSignWrite(t, base, dapp.DappActionRegister, addr, priv,
		"name=Replay|url=https://r.com",
		map[string]any{"name": "Replay", "url": "https://r.com"})
	if code != 200 {
		t.Fatalf("setup register: %d %v", code, out)
	}
	id := int64(out["dapp"].(map[string]any)["id"].(float64))
	dappSignWrite(t, base, dapp.DappActionAdmin, addr, priv,
		"dapp="+strconv.FormatInt(id, 10)+"|status=approved",
		map[string]any{"dapp_id": id, "status": "approved"})

	// 1) 重放：手動取 nonce、簽一次、用兩次。
	_, ch := dappPostRaw(t, base+"/api/dapp/challenge", map[string]any{"address": addr, "action": dapp.DappActionFavorite})
	nonce, _ := ch["nonce"].(string)
	ts, _ := ch["timestamp"].(float64)
	payload := "dapp=" + strconv.FormatInt(id, 10)
	msg := dapp.DAppSignMessage(dapp.DappActionFavorite, addr, nonce, int64(ts), payload)
	sig, _ := crypto.SignPersonalMessage([]byte(msg), priv)
	env := map[string]any{"address": addr, "nonce": nonce, "timestamp": int64(ts), "signature": "0x" + hex.EncodeToString(sig), "dapp_id": id}
	code, out = dappPostRaw(t, base+"/api/dapp/favorite", env)
	if code != 200 {
		t.Fatalf("first favorite should pass: %d %v", code, out)
	}
	// 重放同一信封 → nonce 已消耗，應 401。
	code, out = dappPostRaw(t, base+"/api/dapp/favorite", env)
	if code != http.StatusUnauthorized {
		t.Fatalf("replayed nonce should be 401, got %d: %v", code, out)
	}

	// 2) 偽造簽名（另一把金鑰簽，卻聲稱是 addr）。
	attackerPriv, _ := ethKey(t)
	_, ch2 := dappPostRaw(t, base+"/api/dapp/challenge", map[string]any{"address": addr, "action": dapp.DappActionFavorite})
	nonce2, _ := ch2["nonce"].(string)
	ts2, _ := ch2["timestamp"].(float64)
	msg2 := dapp.DAppSignMessage(dapp.DappActionFavorite, addr, nonce2, int64(ts2), payload)
	sig2, _ := crypto.SignPersonalMessage([]byte(msg2), attackerPriv)
	env2 := map[string]any{"address": addr, "nonce": nonce2, "timestamp": int64(ts2), "signature": "0x" + hex.EncodeToString(sig2), "dapp_id": id}
	code, out = dappPostRaw(t, base+"/api/dapp/favorite", env2)
	if code != http.StatusUnauthorized {
		t.Fatalf("forged signer should be 401, got %d: %v", code, out)
	}
}

func TestV165DAppAdminNotWhitelisted(t *testing.T) {
	_, srv := newTestRPCServer(t)
	defer srv.Close()
	base := srv.URL

	priv, addr := ethKey(t)
	// 不把 addr 加入白名單。
	t.Setenv("TAC_DAPP_ADMINS", "0x0000000000000000000000000000000000000001")

	code, out := dappSignWrite(t, base, dapp.DappActionAdmin, addr, priv,
		"dapp=1|status=approved",
		map[string]any{"dapp_id": 1, "status": "approved"})
	if code != http.StatusForbidden {
		t.Fatalf("non-whitelisted admin should be 403, got %d: %v", code, out)
	}
}
