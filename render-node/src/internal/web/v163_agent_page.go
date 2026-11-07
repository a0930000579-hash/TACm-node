package web

import (
	"net/http"
)

// v164 子任務 B：AI Agent 已併入 TACm（統一更名為 TACm AI / TACm AI）。
//
// 去重說明：v163 的獨立 /agent 頁（function calling 聊天）與 /ai 助手頁已合併為
// 單一入口 GET /ai（ai.html）——同一對話框上方可切換「助手（/api/ai/ask）」與
// 「Agent（/api/ai/agent）」兩種模式，工具呼叫軌跡與寫操作二次確認閘皆保留。
// 為避免出現兩個平行 AI 入口，GET /agent 一律 302 重導向到 /ai，不再 render agent.html。
// 後端介面 POST /api/ai/agent 與 internal/ai 核心保持不變；僅前端入口合一。
//
// （本檔由「子代理 B」完整實作，其它支線不得編輯；server.go 的 GET /agent 路由保留指向此處。）
func (s *Server) handleV163Agent(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ai", http.StatusFound) // 302 → 單一 TACm AI 入口
}
