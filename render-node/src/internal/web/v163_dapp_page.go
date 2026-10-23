package web

import (
	"net/http"

	"github.com/flosch/pongo2/v6"
)

// v163 平行支線掛載點（DApp 平台頁面）。
//
// 本檔由「子代理 A（DApp 平台）」完整實作，其它支線不得編輯。
// 路由 GET /dapp 已預埋於 server.go（指向 handleV163DApp），A 無需改動 server.go/handlers.go。
// 對應模板 templates/dapp.html（A 覆寫占位版本）；頁面專屬 i18n 請走
// window.TAC_LOAD_I18N({zh:{...},en:{...}}) 機制，勿直接改 static/js/i18n.js。
//
// 占位：僅渲染模板，使基線可獨立建置。
func (s *Server) handleV163DApp(w http.ResponseWriter, r *http.Request) {
	s.render(w, "dapp.html", pongo2.Context{})
}
