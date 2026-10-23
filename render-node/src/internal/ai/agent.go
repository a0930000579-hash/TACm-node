package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Step 是 Agent 執行軌跡的一步（前端可摺疊卡片展示）。
type Step struct {
	Kind     string         `json:"kind"` // thought | tool | answer | error | needs_confirm
	Thought  string         `json:"thought,omitempty"`
	ToolName string         `json:"tool,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Result   string         `json:"result,omitempty"`
	Err      string         `json:"error,omitempty"`
	Ts       int64          `json:"ts"`
}

// Result 是一次 Agent.Run 的完整產出。
type Result struct {
	Answer       string `json:"answer"`
	Trace        []Step `json:"trace"`
	NeedsConfirm bool   `json:"needs_confirm"`
	Model        string `json:"model"`
	// Intent 為本輪分類（chat | query | action），供前端顯示與除錯。
	Intent string `json:"intent,omitempty"`
	// v165：聯網搜尋 grounding。無內容時序列化為 false / []（方便前端穩定展開）。
	Searched  bool       `json:"searched"`
	Queries   []string   `json:"queries"`
	Citations []Citation `json:"citations"`
}

// Agent 是 Function-Calling 迴圈主體。
//
// 狀態機：
//
//	[buildMessages] → loop(maxIter)：
//	  Provider.Complete →
//	    ├─ 無 tool_calls → 最終回答 → return
//	    └─ 有 tool_calls → 並行執行（sync.WaitGroup）→
//	         ├─ 寫操作未 confirm → NeedsConfirm（不簽任何交易）→ return
//	         ├─ 工具回錯 → 把錯誤餵回模型（換方法）→ 繼續 loop
//	         └─ 成功 → 結果餵回模型 → 繼續 loop
//
// 防無限迴圈：maxIter（預設 8）；逾時/取消由 ctx 控制；不 panic、不吞錯。
type Agent struct {
	provider Provider
	registry *Registry
	memory   *MemoryStore
	maxIter  int
	timeout  time.Duration
}

// Option 組裝 Agent 的可選參數。
type Option func(*Agent)

// WithMaxIterations 設定最大迭代次數（預設 8）。
func WithMaxIterations(n int) Option {
	return func(a *Agent) {
		if n > 0 {
			a.maxIter = n
		}
	}
}

// WithTimeout 設定整體 Agent 迴圈逾時。
func WithTimeout(d time.Duration) Option {
	return func(a *Agent) {
		if d > 0 {
			a.timeout = d
		}
	}
}

// WithMemory 注入自訂記憶存放（測試可共享）。
func WithMemory(m *MemoryStore) Option {
	return func(a *Agent) {
		if m != nil {
			a.memory = m
		}
	}
}

// NewAgent 組裝 Agent。provider 不可為 nil，registry 不可為 nil。
func NewAgent(provider Provider, registry *Registry, opts ...Option) (*Agent, error) {
	if provider == nil {
		return nil, fmt.Errorf("ai: nil provider")
	}
	if registry == nil {
		return nil, fmt.Errorf("ai: nil registry")
	}
	a := &Agent{
		provider: provider,
		registry: registry,
		memory:   NewMemoryStore(),
		maxIter:  8,
		timeout:  60 * time.Second,
	}
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

// systemPromptAgent Titan Ai 系統提示詞（v165 通用化人設）。
//
// 雙重身分：(1) 通用、人性化 AI 助手；(2) 僅在問題確實關乎 TACm 鏈上數據/DeFi 時，
// 才動用 function-calling 工具。其餘日常／規劃／寫作／解釋／翻譯／學習一律直接自然回答，
// 不硬套工具。金融相關自帶風險提示、不報明牌、寫操作需確認閘。
const systemPromptAgent = "You are Titan Ai (泰坦 Ai), a warm, natural and capable AI assistant on the TAC AI Smart Chain.\n" +
	"LANGUAGE: reply in the user's language — use Traditional Chinese for Chinese, otherwise English.\n" +
	"PERSONA: be conversational, concise and human. When a request is ambiguous, ask one short clarifying " +
	"question instead of guessing. Summarize the key point when helpful, and close with a gentle follow-up " +
	"only when it moves the conversation forward.\n" +
	"YOUR SCOPE: you are a general-purpose assistant covering (a) everyday conversation, life advice, common " +
	"sense and general questions; (b) writing, translation, summarizing, planning and study coaching; and " +
	"(c) software engineering — writing, reading, explaining, refactoring and debugging code, algorithms and " +
	"data structures, in Go/Python/JavaScript/SQL and more; provide fenced code blocks with a concise explanation. " +
	"For all of these, answer directly in natural language and DO NOT call any tool.\n" +
	"TOOLS: you have on-chain tools (get_balance, get_price, get_recent_txs, get_pool, read_memory, " +
	"dex_swap, defi_deposit, defi_borrow). CALL A TOOL ONLY when the user's question is genuinely about " +
	"TACm on-chain data or DeFi actions (wallet address, balances, transactions, TAC price, DEX/DeFi). " +
	"Everything else — everyday life, general knowledge, writing, translation, learning, or programming — " +
	"is answered directly without tools.\n" +
	"FINANCIAL SAFETY: never give investment advice and never name a token as a buy/sell recommendation. " +
	"Always append a brief risk note for financial/DeFi actions; on-chain and DeFi actions are irreversible " +
	"and carry market and smart-contract risk. Write/trade actions require the user's explicit confirmation " +
	"before any transaction is broadcast — the node never signs for the user.\n" +
	"WEB GROUNDING: you also have web_search and web_fetch. Use web_search ONLY when the user needs " +
	"up-to-date information (news, current events, macro/global markets, or a fact you cannot know). " +
	"Do NOT search for static knowledge, everyday chat, or TACm on-chain data (balance/txs/TAC price stay on-chain tools). " +
	"Prefer on-chain price tool for TAC; treat web market data as reference only. When you use web results, list the " +
	"source titles/URLs and separate what came from the web vs. your prior knowledge. If web_search returns unavailable, " +
	"say plainly that you are offline and cannot reach the web — NEVER invent or fabricate links/sources.\n" +
	"ERRORS: when a tool fails, read the error and try a different approach; if you still cannot help, " +
	"say so honestly rather than inventing data."

// Run 執行一次 Agent 迴圈。confirm=false 時，所有 RequiresConfirm 工具不會真正執行。
func (a *Agent) Run(ctx context.Context, userMsg, sessionID string, confirm bool) (*Result, error) {
	if strings.TrimSpace(userMsg) == "" {
		return nil, fmt.Errorf("ai: empty user message")
	}

	// 整體逾時控制。
	runCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	res := &Result{Model: a.provider.Name(), Queries: []string{}, Citations: []Citation{}}

	// 組裝對話：system + 歷史記憶 + 本輪 user。
	history := a.memory.Load(sessionID)
	msgs := []ChatMessage{{Role: "system", Content: systemPromptAgent}}
	msgs = append(msgs, history...)
	msgs = append(msgs, ChatMessage{Role: "user", Content: userMsg})

	// 意圖分類（chat / query / action），供前端標示與除錯；鏈上續問自動併入 query。
	res.Intent = string(resolveIntent(userMsg, history))

	for iter := 0; iter < a.maxIter; iter++ {
		turn, err := a.provider.Complete(runCtx, msgs, a.registry.List())
		if err != nil {
			// ctx 取消/逾時直接返回（不餵模型重試）。
			if runCtx.Err() != nil {
				return nil, fmt.Errorf("ai: agent cancelled/timed out: %w", runCtx.Err())
			}
			// 模型層錯誤：第一次就失敗 → 返回；後續輪失敗 → 餵錯給模型換方法一次。
			if iter == 0 {
				return nil, fmt.Errorf("ai: provider complete: %w", err)
			}
			res.Trace = append(res.Trace, Step{Kind: "error", Err: err.Error(), Ts: nowTS()})
			msgs = append(msgs, ChatMessage{Role: "system", Content: "Previous step failed: " + err.Error() + ". Choose another tool or answer."})
			continue
		}

		if turn.Thought != "" {
			res.Trace = append(res.Trace, Step{Kind: "thought", Thought: turn.Thought, Ts: nowTS()})
		}

		if !turn.HasToolCalls() {
			// 最終回答。
			res.Answer = strings.TrimSpace(turn.Answer)
			res.Trace = append(res.Trace, Step{Kind: "answer", Result: res.Answer, Ts: nowTS()})
			a.memory.Append(sessionID,
				ChatMessage{Role: "user", Content: userMsg},
				ChatMessage{Role: "assistant", Content: res.Answer},
			)
			return res, nil
		}

		// 把 assistant 的 tool_calls 記進對話，並行執行。
		msgs = append(msgs, ChatMessage{Role: "assistant", Content: turn.Thought, ToolCalls: turn.ToolCalls})
		outcomes := a.registry.ExecCalls(runCtx, turn.ToolCalls, confirm)

		// 執行後檢查整體逾時/取消：即使 provider 不主動取消，工具階段觸達逾時也要立即返回。
		if cerr := runCtx.Err(); cerr != nil {
			return nil, fmt.Errorf("ai: agent cancelled/timed out during tool execution: %w", cerr)
		}

		var needsConfirm bool
		var blockedActions []string
		for _, oc := range outcomes {
			step := Step{Kind: "tool", ToolName: oc.Call.Name, Args: oc.Call.Args, Ts: nowTS()}
			if oc.SkippedNeedsConfirm {
				needsConfirm = true
				step.Kind = "needs_confirm"
				step.Err = oc.Err
				blockedActions = append(blockedActions, describePlannedAction(oc.Call))
			} else if oc.Err != "" {
				step.Err = oc.Err
			} else {
				step.Result = oc.Result.String()
				res.absorbCitations(oc)
			}
			res.Trace = append(res.Trace, step)

			// 把結果餵回對話（role=tool）。
			content := oc.Err
			if oc.Err == "" {
				content = oc.Result.String()
			}
			msgs = append(msgs, ChatMessage{
				Role:       "tool",
				Name:       oc.Call.Name,
				ToolCallID: oc.Call.ID,
				Content:    content,
			})
		}

		if needsConfirm {
			// 硬性安全：未確認不簽任何交易。直接回 NeedsConfirm，不再呼叫模型。
			res.NeedsConfirm = true
			res.Answer = confirmMessage(blockedActions, confirm)
			res.Trace = append(res.Trace, Step{Kind: "answer", Result: res.Answer, Ts: nowTS()})
			return res, nil
		}
		// 否則繼續迴圈，讓模型依工具結果決定下一步。
	}

	// 超過最大迭代仍未收斂。
	res.Answer = "I've reached the step limit while working on your request. Please narrow it down or rephrase."
	res.Trace = append(res.Trace, Step{Kind: "answer", Result: res.Answer, Ts: nowTS()})
	return res, nil
}

// absorbCitations 從 web_search 工具結果提取「下過哪些查詢」與「引用來源」。
// 僅在真的有結果（citations）時才標 Searched=true——離線/零結果不宣稱有來源。
func (r *Result) absorbCitations(oc ToolCallOutcome) {
	if oc.Call.Name != "web_search" {
		return
	}
	m, ok := oc.Result.Data.(map[string]any)
	if !ok {
		return
	}
	if qs, ok := m["queries"].([]string); ok {
		r.Queries = append(r.Queries, qs...)
	}
	if cs, ok := m["citations"].([]SearchResult); ok && len(cs) > 0 {
		r.Searched = true
		for _, c := range cs {
			r.Citations = append(r.Citations, Citation{Title: c.Title, URL: c.URL})
		}
	}
}

// describePlannedAction 把一次被攔截的寫操作描述給使用者看。
func describePlannedAction(call ToolCall) string {
	b, _ := json.Marshal(call.Args)
	return fmt.Sprintf("%s(%s)", call.Name, string(b))
}

// confirmMessage 組裝需確認的提示訊息。
func confirmMessage(actions []string, confirm bool) string {
	if confirm {
		// 理論上 confirm=true 不會進這裡（因為 execCall 會放行）；保險起見。
		return "Action was prepared."
	}
	return "This is a write action and requires your confirmation before any transaction is broadcast.\n" +
		"Planned: " + strings.Join(actions, "; ") + "\n" +
		"Please press Confirm to proceed. Risk: crypto actions are irreversible; never confirm amounts you cannot afford."
}
