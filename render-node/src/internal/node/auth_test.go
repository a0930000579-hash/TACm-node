package node

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthLifecycle(t *testing.T) {
	os.Setenv("TAC_AUTH_SECRET", "test-secret")
	defer os.Unsetenv("TAC_AUTH_SECRET")
	svc, err := OpenAuth(filepath.Join(t.TempDir()))
	if err != nil {
		t.Fatalf("OpenAuth: %v", err)
	}
	defer svc.Close()
	// 註冊
	uid, token, _, err := svc.Register("alice@example.com", "secret123", "")
	if err != nil || uid <= 0 {
		t.Fatalf("Register: uid=%d err=%v", uid, err)
	}
	// 重複 email 拒
	if _, _, _, err := svc.Register("Alice@Example.com", "secret123", ""); err == nil {
		t.Fatalf("重複 email 應拒")
	}
	// 壞 email / 短密碼拒
	if _, _, _, err := svc.Register("bad", "secret123", ""); err == nil {
		t.Fatalf("壞 email 應拒")
	}
	if _, _, _, err := svc.Register("bob@example.com", "123", ""); err == nil {
		t.Fatalf("短密碼應拒")
	}
	// 登入成功
	uid2, tok2, err := svc.Login("alice@example.com", "secret123")
	if err != nil || uid2 != uid {
		t.Fatalf("Login: %v uid=%d", err, uid2)
	}
	// 錯誤密碼拒
	if _, _, err := svc.Login("alice@example.com", "wrong"); err == nil {
		t.Fatalf("錯誤密碼應拒")
	}
	// session 驗證
	u, err := svc.VerifySession(tok2)
	if err != nil || u.Email != "alice@example.com" {
		t.Fatalf("VerifySession: %v %+v", err, u)
	}
	// cookie 流程
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
	cur, err := svc.CurrentUser(req)
	if err != nil || cur == nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	// 偽造 token 拒
	if _, err := svc.VerifySession("deadbeef.fake"); err == nil {
		t.Fatalf("偽造 token 應拒")
	}
	_ = uid2
}
