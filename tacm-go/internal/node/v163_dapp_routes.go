package node

// v163/v165 DApp 平台 RPC（對標 Ethereum DApp 入口 / DApp 商店）：
//   POST /api/dapp/challenge   — 核發一次性 nonce（寫操作前，前端先取 nonce 再簽名）
//   POST /api/dapp/register   — 註冊／更新 DApp（需 EIP-191 簽名防冒名；新註冊為 pending）
//   GET  /api/dapp/list       — 列表（公開僅 approved/featured）
//   GET  /api/dapp/detail?id= — 單一 DApp 詳情（並記一次瀏覽）
//   GET  /api/dapp/categories — 所有分類
//   POST /api/dapp/rate       — 評分（需簽名）
//   POST /api/dapp/favorite  — 切換收藏（需簽名）
//   GET  /api/dapp/favorites?address= — 我的收藏
//   GET  /api/dapp/mine?deployer=     — 我部署的 DApp（含 pending）
//   POST /api/dapp/admin/status — 審核上架（deployer 白名單 TAC_DAPP_ADMINS，需簽名）
// 業務狀態與簽名驗證在 internal/dapp；此層只做參數校驗、JSON 回覆。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/dapp"
)

// registerV163DAppRoutes 由 rpc.go 於 registerRoutes 流程呼叫（勿改 rpc.go）。
func (s *RPCServer) registerV163DAppRoutes() {
	s.addRouteDoc("POST /api/dapp/challenge", "Issue a one-time signing nonce for a DApp write op", s.handleDAppChallenge)
	s.addRouteDoc("POST /api/dapp/register", "Register or update a DApp (EIP-191 signed; new = pending)", s.handleDAppRegister)
	s.addRouteDoc("GET /api/dapp/list", "List DApps with search/category/sort/paging", s.handleDAppList)
	s.addRouteDoc("GET /api/dapp/detail", "Get a single DApp detail (records a view)", s.handleDAppDetail)
	s.addRouteDoc("GET /api/dapp/categories", "List all categories", s.handleDAppCategories)
	s.addRouteDoc("POST /api/dapp/rate", "Rate a DApp 1-5 (EIP-191 signed, same address can update)", s.handleDAppRate)
	s.addRouteDoc("POST /api/dapp/favorite", "Toggle favorite for a DApp (EIP-191 signed)", s.handleDAppFavorite)
	s.addRouteDoc("GET /api/dapp/favorites", "List my favorite DApps", s.handleDAppFavorites)
	s.addRouteDoc("GET /api/dapp/mine", "List DApps I deployed (includes pending)", s.handleDAppMine)
	s.addRouteDoc("POST /api/dapp/admin/status", "Admin review: set pending/approved/featured (whitelisted signer)", s.handleDAppAdminStatus)
}

// dappDisabled 統一回「未啟用」。
func dappDisabled(w http.ResponseWriter) {
	writeErr(w, http.StatusServiceUnavailable, "DApp store 未啟用")
}

// dappSigEnvelope 寫操作共用的簽名信封。
type dappSigEnvelope struct {
	Address   string `json:"address"`
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

// handleDAppChallenge POST /api/dapp/challenge
// body: { "address": "0x…|tx0…", "action": "register|rate|favorite|admin" }
// 回應: { "ok": true, "nonce": "…", "timestamp": 1700000000, "message": "<可簽訊息樣板>" }
func (s *RPCServer) handleDAppChallenge(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	var req struct {
		Address string `json:"address"`
		Action  string `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	addr := strings.TrimSpace(req.Address)
	if addr == "" {
		writeErr(w, http.StatusBadRequest, "缺少 address")
		return
	}
	nonce, ts, err := s.node.dappSt.IssueNonce(addr, strings.TrimSpace(req.Action))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"nonce":     nonce,
		"timestamp": ts,
		"message":   "Sign this exact message with your wallet, then submit the write op with address/nonce/timestamp/signature.",
	})
}

// handleDAppRegister POST /api/dapp/register
func (s *RPCServer) handleDAppRegister(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	var req struct {
		dappSigEnvelope
		Name        string `json:"name"`
		Category    string `json:"category"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Icon        string `json:"icon"`
		Contract    string `json:"contract"`
		Tags        string `json:"tags"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	payload := fmt.Sprintf("name=%s|url=%s", strings.TrimSpace(req.Name), strings.TrimSpace(req.URL))
	if err := s.node.dappSt.VerifyWriteSignature(dapp.DappActionRegister, req.Address, req.Nonce, req.Timestamp, payload, req.Signature); err != nil {
		writeErr(w, http.StatusUnauthorized, "簽名驗證失敗: "+err.Error())
		return
	}
	deployer, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "deployer 地址無效")
		return
	}
	d, err := s.node.dappSt.Register(deployer, req.Name, req.Category, req.Description,
		req.URL, req.Icon, req.Contract, req.Tags)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dapp": d})
}

// handleDAppList GET /api/dapp/list
func (s *RPCServer) handleDAppList(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	res, err := s.node.dappSt.List(dapp.ListParams{
		Q:        q.Get("q"),
		Category: q.Get("category"),
		Sort:     q.Get("sort"),
		Page:     page,
		Size:     size,
		Deployer: q.Get("deployer"),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

// handleDAppDetail GET /api/dapp/detail?id=
func (s *RPCServer) handleDAppDetail(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	d, err := s.node.dappSt.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	// 未審核的 DApp 僅 deployer 本人可見詳情；其他人 404。
	if d.Status != "approved" && d.Status != "featured" {
		if owner := r.URL.Query().Get("deployer"); owner == "" || !strings.EqualFold(owner, d.Deployer) {
			writeErr(w, http.StatusNotFound, "dapp: 尚未上架")
			return
		}
	}
	// 盡力記一次瀏覽（熱門維度）；失敗不影響回傳。
	_ = s.node.dappSt.AddView(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dapp": d})
}

// handleDAppCategories GET /api/dapp/categories
func (s *RPCServer) handleDAppCategories(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	cats, err := s.node.dappSt.Categories()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "categories": cats})
}

// handleDAppRate POST /api/dapp/rate
func (s *RPCServer) handleDAppRate(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	var req struct {
		dappSigEnvelope
		DAppID  int64  `json:"dapp_id"`
		Score   int64  `json:"score"`
		Comment string `json:"comment"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	payload := fmt.Sprintf("dapp=%d|score=%d", req.DAppID, req.Score)
	if err := s.node.dappSt.VerifyWriteSignature(dapp.DappActionRate, req.Address, req.Nonce, req.Timestamp, payload, req.Signature); err != nil {
		writeErr(w, http.StatusUnauthorized, "簽名驗證失敗: "+err.Error())
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	rating, err := s.node.dappSt.Rate(req.DAppID, addr, req.Score, req.Comment)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, _ := s.node.dappSt.Get(req.DAppID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rating": rating, "dapp": d})
}

// handleDAppFavorite POST /api/dapp/favorite
func (s *RPCServer) handleDAppFavorite(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	var req struct {
		dappSigEnvelope
		DAppID int64 `json:"dapp_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	payload := fmt.Sprintf("dapp=%d", req.DAppID)
	if err := s.node.dappSt.VerifyWriteSignature(dapp.DappActionFavorite, req.Address, req.Nonce, req.Timestamp, payload, req.Signature); err != nil {
		writeErr(w, http.StatusUnauthorized, "簽名驗證失敗: "+err.Error())
		return
	}
	addr, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	nowFav, d, err := s.node.dappSt.FavoriteToggle(req.DAppID, addr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "favorited": nowFav, "dapp": d})
}

// handleDAppFavorites GET /api/dapp/favorites?address=
func (s *RPCServer) handleDAppFavorites(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	addr, err := s.node.normalizeEthAddr(r.URL.Query().Get("address"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "address 無效")
		return
	}
	items, err := s.node.dappSt.Favorites(addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

// handleDAppMine GET /api/dapp/mine?deployer=
func (s *RPCServer) handleDAppMine(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	addr, err := s.node.normalizeEthAddr(r.URL.Query().Get("deployer"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "deployer 地址無效")
		return
	}
	res, err := s.node.dappSt.List(dapp.ListParams{
		Deployer:          addr,
		Sort:              "new",
		Size:              100,
		IncludeUnreviewed: true, // deployer 本人可見自己的 pending
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

// handleDAppAdminStatus POST /api/dapp/admin/status
// body: { address, nonce, timestamp, signature, dapp_id, status }
// 授權：address 須在 TAC_DAPP_ADMINS 白名單，且簽名驗證通過。
func (s *RPCServer) handleDAppAdminStatus(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	var req struct {
		dappSigEnvelope
		DAppID int64  `json:"dapp_id"`
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	// 白名單授權（比對簽名前先檢查，避免耗費 recover）。
	if !dapp.IsDAppAdmin(req.Address) {
		writeErr(w, http.StatusForbidden, "非審核白名單地址")
		return
	}
	payload := fmt.Sprintf("dapp=%d|status=%s", req.DAppID, strings.TrimSpace(req.Status))
	if err := s.node.dappSt.VerifyWriteSignature(dapp.DappActionAdmin, req.Address, req.Nonce, req.Timestamp, payload, req.Signature); err != nil {
		writeErr(w, http.StatusUnauthorized, "簽名驗證失敗: "+err.Error())
		return
	}
	d, err := s.node.dappSt.SetStatus(req.DAppID, req.Status)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dapp": d})
}
