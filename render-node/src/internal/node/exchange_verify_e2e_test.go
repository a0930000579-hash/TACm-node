package node

// v158：交易所「驗證地址」端對端測試。
// 驗證地址 = 登入會員綁定的鏈上錢包地址（與礦機 M37 綁定同源 user.wallet_addr）。
// 場景：未登入需登入；空 uid 自動回會員地址；帶 uid 一致性校驗。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestExchangeVerifyAddress(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	base := srv.URL

	// 1) 未登入 → need_login。
	r := postJSON(t, base+"/api/exchange/verify-address", map[string]any{})
	if _, ok := r["need_login"]; !ok {
		t.Fatalf("未登入應 need_login，got: %v", r)
	}

	// 2) 註冊會員（authSvc 與 RPC 同源）。
	uid, token, addr, err := n.authSvc.Register("exverify@example.com", "secret123", "")
	if err != nil || uid <= 0 || addr == "" {
		t.Fatalf("Register: uid=%d addr=%q err=%v", uid, addr, err)
	}

	doReq := func(body string) map[string]any {
		req, err := http.NewRequest("POST", base+"/api/exchange/verify-address", strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", authCookieName+"="+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return m
	}

	// 3) 登入 + 空 uid → verified=true 且回傳會員綁定地址。
	r = doReq(`{}`)
	if ok, _ := r["ok"].(bool); !ok {
		t.Fatalf("空 uid 應 ok，got: %v", r)
	}
	if v, _ := r["verified"].(bool); !v {
		t.Fatalf("空 uid 應 verified=true，got: %v", r)
	}
	if a, _ := r["address"].(string); a != addr {
		t.Fatalf("address 應等於會員綁定地址 %q，got %q", addr, a)
	}

	// 4) 帶正確 uid → verified=true。
	r = doReq(`{"uid":"` + addr + `"}`)
	if v, _ := r["verified"].(bool); !v {
		t.Fatalf("正確 uid 應 verified=true，got: %v", r)
	}

	// 5) 帶錯誤 uid → verified=false（不鎖死，僅標不一致）。
	r = doReq(`{"uid":"tx0DEADBEEFdeadbeef"}`)
	if v, _ := r["verified"].(bool); v {
		t.Fatalf("錯誤 uid 應 verified=false，got: %v", r)
	}
}
