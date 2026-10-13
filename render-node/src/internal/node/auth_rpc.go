package node

import (
	"encoding/json"
	"io"
	"net/http"
)

// ===== 會員 RPC（對應 Python /api/auth/*）=====

// handleAuthRegister 註冊：email＋password，成功即登入（設定 cookie）。
func (s *RPCServer) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Email        string `json:"email"`
		Password     string `json:"password"`
		ReferralCode string `json:"referral_code"`
	}
	if err := decodeJSON(r, &p); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": "參數格式錯誤"})
		return
	}
	uid, token, walletAddr, err := s.node.authSvc.Register(p.Email, p.Password, p.ReferralCode)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	// M32：推薦關係記錄（提升邀請人礦機算力）。
	if p.ReferralCode != "" && s.node.walletSvc != nil {
		_ = s.node.walletSvc.Store().AddReferral(p.ReferralCode, walletAddr)
	}
	SetAuthCookie(w, token)
	writeJSON(w, 200, map[string]any{"ok": true, "uid": uid, "email": p.Email, "wallet_addr": walletAddr})
}

// handleAuthLogin 登入：驗證並設定 cookie。
func (s *RPCServer) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &p); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": "參數格式錯誤"})
		return
	}
	uid, token, err := s.node.authSvc.Login(p.Email, p.Password)
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	SetAuthCookie(w, token)
	writeJSON(w, 200, map[string]any{"ok": true, "uid": uid, "email": p.Email})
}

// handleAuthLogout 登出：清除 cookie。
func (s *RPCServer) handleAuthLogout(w http.ResponseWriter, _ *http.Request) {
	ClearAuthCookie(w)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleAuthMe 目前登入使用者（cookie）。
func (s *RPCServer) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.node.authSvc.CurrentUser(r)
	if err != nil {
		writeJSON(w, 401, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	if u == nil {
		writeJSON(w, 200, map[string]any{"ok": true, "logged_in": false})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "logged_in": true, "user": u})
}

func decodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return json.Unmarshal([]byte("{}"), v)
	}
	return json.Unmarshal(body, v)
}
