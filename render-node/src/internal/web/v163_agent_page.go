package web

import (
	"net/http"

	"github.com/flosch/pongo2/v6"
)

// v163 平行支線掛載點（AI Agent 聊天頁面）。
//
// 本檔由「子代理 B（AI Agent）」完整實作，其它支線不得編輯。
// 路由 GET /agent 已預埋於 server.go（指向 handleV163Agent），B 無需改動 server.go/handlers.go。
// B 可一併擴充 templates/ai.html（加入 Agent 入口/分頁，ai.html 由 B 負責）。
// 對應模板 templates/agent.html（B 覆寫占位版本）；頁面專屬 i18n 請走
// window.TAC_LOAD_I18N({zh:{...},en:{...}}) 機制，勿直接改 static/js/i18n.js。
//
// 占位：僅渲染模板，使基線可獨立建置。
func (s *Server) handleV163Agent(w http.ResponseWriter, r *http.Request) {
	s.render(w, "agent.html", pongo2.Context{})
}
