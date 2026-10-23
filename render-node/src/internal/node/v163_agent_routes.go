package node

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"tacm/internal/ai"
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
}

type agentRequest struct {
	Message   string `json:"message"`
	Address   string `json:"address"`
	Lang      string `json:"lang"`
	SessionID string `json:"session_id"`
	Confirm   bool   `json:"confirm"`
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
	reg := s.buildAgentRegistry(func() string { return mem.Summary(req.SessionID) })

	// Provider：llama 在線走原生 function calling（best-effort）；失敗自動退回離線規則。
	base := ai.NewRulesProvider(req.Address)
	var provider ai.Provider = base
	if probeLLM() {
		if lp, err := ai.NewLlamaProvider(llm()); err == nil {
			provider = ai.NewFallbackProvider(lp, base)
		}
	}

	agent, err := ai.NewAgent(provider, reg, ai.WithMemory(mem), ai.WithMaxIterations(8), ai.WithTimeout(60*time.Second))
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
	for _, st := range result.Trace {
		steps = append(steps, agentStepDTO{
			Kind: st.Kind, Thought: st.Thought, ToolName: st.ToolName, Args: st.Args,
			Result: st.Result, Err: st.Err, Ts: st.Ts,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "answer": result.Answer, "model": result.Model,
		"needs_confirm": result.NeedsConfirm, "trace": steps,
	})
}
