package node

// v163 DApp 平台 RPC（對標 Ethereum DApp 入口 / DApp 商店）：
//   POST /api/dapp/register   — 註冊／更新 DApp（同名同 deployer 版本遞增）
//   GET  /api/dapp/list       — 列表（q/category/sort/page/size/deployer）
//   GET  /api/dapp/detail?id= — 單一 DApp 詳情（並記一次瀏覽）
//   GET  /api/dapp/categories — 所有分類
//   POST /api/dapp/rate       — 評分（同 address 對同 dapp 可更新，不重複計數）
//   POST /api/dapp/favorite   — 切換收藏
//   GET  /api/dapp/favorites?address= — 我的收藏
//   GET  /api/dapp/mine?deployer=    — 我部署的 DApp
// 業務狀態在 internal/dapp；此層只做參數校驗、地址標準化與 JSON 回覆。

import (
	"net/http"
	"strconv"

	"tacm/internal/dapp"
)

// registerV163DAppRoutes 由 rpc.go 於 registerRoutes 流程呼叫（勿改 rpc.go）。
func (s *RPCServer) registerV163DAppRoutes() {
	s.addRouteDoc("POST /api/dapp/register", "Register or update a DApp", s.handleDAppRegister)
	s.addRouteDoc("GET /api/dapp/list", "List DApps with search/category/sort/paging", s.handleDAppList)
	s.addRouteDoc("GET /api/dapp/detail", "Get a single DApp detail (records a view)", s.handleDAppDetail)
	s.addRouteDoc("GET /api/dapp/categories", "List all categories", s.handleDAppCategories)
	s.addRouteDoc("POST /api/dapp/rate", "Rate a DApp 1-5 (same address can update)", s.handleDAppRate)
	s.addRouteDoc("POST /api/dapp/favorite", "Toggle favorite for a DApp", s.handleDAppFavorite)
	s.addRouteDoc("GET /api/dapp/favorites", "List my favorite DApps", s.handleDAppFavorites)
	s.addRouteDoc("GET /api/dapp/mine", "List DApps I deployed", s.handleDAppMine)
}

// dappStoreNil 統一回「未啟用」。
func dappDisabled(w http.ResponseWriter) {
	writeErr(w, http.StatusServiceUnavailable, "DApp store 未啟用")
}

// handleDAppRegister POST /api/dapp/register
func (s *RPCServer) handleDAppRegister(w http.ResponseWriter, r *http.Request) {
	if s.node.dappSt == nil {
		dappDisabled(w)
		return
	}
	var req struct {
		Address     string `json:"address"`
		Name        string `json:"name"`
		Category    string `json:"category"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Icon        string `json:"icon"`
		Contract    string `json:"contract"`
		Tags        string `json:"tags"`
		Featured    bool   `json:"featured"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
		return
	}
	deployer, err := s.node.normalizeEthAddr(req.Address)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "deployer 地址無效")
		return
	}
	d, err := s.node.dappSt.Register(deployer, req.Name, req.Category, req.Description,
		req.URL, req.Icon, req.Contract, req.Tags, req.Featured)
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
		Address string `json:"address"`
		DAppID  int64  `json:"dapp_id"`
		Score   int64  `json:"score"`
		Comment string `json:"comment"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
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
		Address string `json:"address"`
		DAppID  int64  `json:"dapp_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json")
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
	res, err := s.node.dappSt.List(dapp.ListParams{Deployer: addr, Sort: "new", Size: 100})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}
