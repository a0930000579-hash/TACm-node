package web

import (
	"net/http"

	"github.com/flosch/pongo2/v6"
)

// v186：獨立 Agent 視窗。
//
// GET /agent 不再 302 重導向，而是 render 全螢幕 standalone 的 agent-app.html
// （不繼承 base.html、自有完整 HTML 文件）：可收合側邊欄（新對話／對話歷史／模型選擇／
// 計費）、主流 AI App 風格、行動優先、中英切換。後端對話仍走 POST /api/ai/agent，
// 工具軌跡、寫操作二次確認閘與 TACm 計費全部保留。
//
// 舊 /ai（ai.html，站內殼層）保留不動，作為站內入口；/agent 為獨立視窗。
func (s *Server) handleV163Agent(w http.ResponseWriter, r *http.Request) {
	s.render(w, "agent-app.html", pongo2.Context{})
}
