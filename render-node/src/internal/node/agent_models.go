package node

import (
	"net/http"
	"os"

	"tacm/internal/ai"
)

// v186 子任務：本地模型與介面（task 271）——唯讀模型狀態端點。
// GET /api/ai/models
// resp: {ok, active, providers:[{id,name,kind,online,endpoint,detail}]}
//
// 只回報「提供者是否在線／端點／模型名」等可公開狀態，絕不回傳 API key 或任何私鑰材料。
// 前端獨立 Agent 視窗據此做模型選擇、在線/離線標示與健康提示。

func (s *RPCServer) registerAgentModelRoutes() {
	s.addRoute("GET /api/ai/models", s.handleAIModels)
}

// modelProviderDTO 單一模型提供者的狀態。
type modelProviderDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Online   bool   `json:"online"`
	Endpoint string `json:"endpoint,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func (s *RPCServer) handleAIModels(w http.ResponseWriter, r *http.Request) {
	providers := make([]modelProviderDTO, 0, 3)

	// 1) OpenAI 相容外部提供者（TAC_LLM_* env）；三者齊備才在線。只取模型名，不回 key。
	openaiOnline := false
	openaiModel := ""
	if op, err := ai.NewOpenAIProviderFromEnv(); err == nil && op != nil {
		openaiOnline = true
		openaiModel = os.Getenv("TAC_LLM_MODEL")
	}
	providers = append(providers, modelProviderDTO{
		ID: "openai", Name: openaiModel, Kind: "openai_compatible",
		Online: openaiOnline, Endpoint: os.Getenv("TAC_LLM_BASE_URL"),
		Detail: envOrBlank("TAC_LLM_BASE_URL"),
	})

	// 2) 本地 llama.cpp（llama-server，OpenAI 兼容）；即時健康探測（帶節奏快取）。
	llamaOnline := probeLLM()
	providers = append(providers, modelProviderDTO{
		ID: "llama", Name: "local-gguf", Kind: "local_llama_cpp",
		Online: llamaOnline, Endpoint: llm().Endpoint,
	})

	// 3) 離線規則提供者：永遠在線，免金鑰保證 AI 一定可用。
	providers = append(providers, modelProviderDTO{
		ID: "rules", Name: "rules", Kind: "offline_rules", Online: true,
	})

	// 當前實際生效的提供者（與 buildAIProvider 相同優先序：openai > llama > rules）。
	active := "rules"
	if llamaOnline {
		active = "llama"
	}
	if openaiOnline {
		active = "openai"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "active": active, "providers": providers,
	})
}

func envOrBlank(key string) string {
	if v := os.Getenv(key); v != "" {
		return "configured"
	}
	return ""
}
