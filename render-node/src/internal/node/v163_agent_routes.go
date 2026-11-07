package node

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"tacm/internal/ai"
	"tacm/internal/wallet"
)

// v163 平行支線：AI Agent（Function Calling）端點。
// POST /api/ai/agent
// body: {message, address?, lang?, session_id?, confirm?}
// resp: {ok, answer, model, needs_confirm, trace:[{kind,thought,tool,args,result,error,ts}]}
//
// Provider 選擇：llama.cpp（原生 function calling）在線時優先；否則離線規則 provider，
// 免金鑰可跑可演示。寫操作工具未帶 confirm=true 一律停在 needs_confirm（不簽/不廣播交易）。

func (s *RPCServer) registerV163AgentRoutes() {
	s.addRoute("POST /api/ai/agent", s.handleAIAgent)
	s.addRoute("GET /api/ai/file/{id}", s.handleAIDownload)
}

type agentRequest struct {
	Message   string `json:"message"`
	Address   string `json:"address"`
	Lang      string `json:"lang"`
	SessionID string `json:"session_id"`
	Confirm   bool   `json:"confirm"`
	Model     string `json:"model"`
}

type agentStepDTO struct {
	Kind     string         `json:"kind"`
	Thought  string         `json:"thought,omitempty"`
	ToolName string         `json:"tool,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Result   string         `json:"result,omitempty"`
	Err      string         `json:"error,omitempty"`
	Ts       int64          `json:"ts"`
}

func (s *RPCServer) handleAIAgent(w http.ResponseWriter, r *http.Request) {
	var req agentRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<(10+10))) // 1 MiB 上限
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json: "+err.Error())
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeErr(w, http.StatusBadRequest, "missing_message: 請輸入問題")
		return
	}
	if len(req.Message) > 500 {
		req.Message = req.Message[:500]
	}
	if req.SessionID == "" {
		req.SessionID = "default"
	}
	if req.Lang == "" {
		req.Lang = "en"
	}

	mem := sharedAgentMemory()
	reg := s.buildAgentRegistry(func() string { return mem.Summary(req.SessionID) }, req.Address)

	// Provider 依使用者選擇的模型組裝；所選模型離線時自動降級到最強可用者（rules 永遠在線）。
	provider, effectiveModel := s.buildAIProviderFor(req.Model, req.Address)

	agent, err := ai.NewAgent(provider, reg,
		ai.WithMemory(mem), ai.WithMaxIterations(8), ai.WithTimeout(60*time.Second),
		ai.WithAddress(req.Address))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "agent_init: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	result, err := agent.Run(ctx, req.Message, req.SessionID, req.Confirm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "agent_run: "+err.Error())
		return
	}

	steps := make([]agentStepDTO, 0, len(result.Trace))
	fileGenerated := false
	for _, st := range result.Trace {
		steps = append(steps, agentStepDTO{
			Kind: st.Kind, Thought: st.Thought, ToolName: st.ToolName, Args: st.Args,
			Result: st.Result, Err: st.Err, Ts: st.Ts,
		})
		if st.Kind == "tool" && st.ToolName == "create_file" && st.Err == "" {
			fileGenerated = true
		}
	}

	// 確認閘未過（needs_confirm）不計費：尚未真正提供服務，避免對未執行動作收費。
	billing := titanBilling{Asset: string(wallet.AssetTACm), FeeAccount: titanFeeAccount}
	if !result.NeedsConfirm {
		b, err := s.node.meterAgentTurn(req.Address, fileGenerated, effectiveModel)
		if err != nil {
			writeErr(w, http.StatusPaymentRequired, "billing_failed: "+err.Error())
			return
		}
		billing = b
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "answer": result.Answer, "model": effectiveModel,
		"needs_confirm": result.NeedsConfirm, "trace": steps,
		"intent":    result.Intent,
		"searched":  result.Searched,
		"queries":   result.Queries,
		"citations": result.Citations,
		"billing":   billing,
	})
}

// aiProviderLogOnce 全進程只記一次 provider 選擇鏈（env 固定，避免每請求洗版）。
var aiProviderLogOnce sync.Once

// buildAIProviderFor 依使用者選擇的模型組裝 provider，並回傳實際生效的模型 id。
// 可用性：openai（TAC_LLM_* env）、llama（本地 llama-server 探測）、rules（永遠在線）。
// 所選模型缺失或離線時，降級到最強可用者，並以其為 fallback 保證運行期錯誤仍可收斂。
func (s *RPCServer) buildAIProviderFor(reqModel, addr string) (ai.Provider, string) {
	base := ai.NewRulesProvider(addr)

	// 各 provider 可用性（不回傳 key）。
	var op ai.Provider
	openaiOn := false
	openaiErr := ""
	if o, err := ai.NewOpenAIProviderFromEnv(); err == nil {
		op = o
		openaiOn = true
	} else {
		openaiErr = err.Error()
	}
	var lp ai.Provider
	llamaOn := false
	if probeLLM() {
		if l, err := ai.NewLlamaProvider(llm()); err == nil {
			lp = l
			llamaOn = true
		}
	}

	// 最強可用者（降級順序 openai > llama > rules）。
	best := "rules"
	if llamaOn {
		best = "llama"
	}
	if openaiOn {
		best = "openai"
	}

	// 實際生效模型：請求有效且在線才採用，否則降級。
	effective := best
	switch reqModel {
	case "openai":
		if openaiOn {
			effective = "openai"
		}
	case "llama":
		if llamaOn {
			effective = "llama"
		}
	case "rules":
		effective = "rules"
	}

	// 構建分層 fallback，保證運行期錯誤（如 llama 404）能一路降級到離線規則，絕不硬失敗。
	// llamaChain：本地 llama 入口，失敗回退 rules；fullChain：openai 入口，失敗回退 llama 再 rules。
	llamaChain := ai.Provider(base)
	if llamaOn {
		llamaChain = ai.NewFallbackProvider(lp, base)
	}
	fullChain := llamaChain
	if openaiOn {
		fullChain = ai.NewFallbackProvider(op, llamaChain)
	}
	var provider ai.Provider
	switch effective {
	case "openai":
		provider = fullChain
	case "llama":
		provider = llamaChain
	default:
		provider = base
	}

	aiProviderLogOnce.Do(func() {
		log.Printf("[ai] provider chain: openai_env=%v llama_online=%v rules=always; best=%s",
			openaiOn, llamaOn, best)
		if openaiErr != "" {
			log.Printf("[ai] openai-compatible provider NOT enabled: %s", openaiErr)
		}
	})
	return provider, effective
}
