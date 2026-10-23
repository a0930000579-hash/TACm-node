// Package ai：v163 AI Agent（Function Calling / Tool Use 迴圈）核心。
//
// 本檔定義工具（Tool）抽象、Registry、參數校驗與派發，以及無依賴工具的並行執行。
// 設計原則（Go 慣用）：
//   - Tool.Run 回 (ToolResult, error)，錯誤由呼叫端（Agent）餵回模型換方法重試；
//   - 並行執行僅用標準庫 sync（WaitGroup/channel），不引入新依賴；
//   - Registry 以 RWMutex 保護，支援執行期動態註冊，不 panic、不吞錯。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Tool 是 Agent 可呼叫的一個能力。Parameters 為 OpenAI function calling 的 JSON Schema。
// RequiresConfirm=true 的寫操作工具，未經呼叫端 confirm 不得執行 Run（由 Agent 層硬性把關）。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	// Run 執行工具。ctx 由 Agent 控制（逾時/取消）；args 已完成 JSON 反序列化與基本校驗。
	Run func(ctx context.Context, args map[string]any) (ToolResult, error)
	// RequiresConfirm 標示寫操作（兌換/存款/借款/下單/轉帳）。
	RequiresConfirm bool `json:"requires_confirm"`
}

// ToolResult 為工具執行結果。Summary 為給模型/使用者看的簡述；Data 為結構化資料。
type ToolResult struct {
	Summary string `json:"summary"`
	Data    any    `json:"data,omitempty"`
}

// MarshalSummary 把 ToolResult 序列化為餵回模型的純文字。
func (r ToolResult) String() string {
	if r.Data == nil {
		return r.Summary
	}
	b, err := json.Marshal(r.Data)
	if err != nil {
		return r.Summary
	}
	return r.Summary + "\n" + string(b)
}

// Registry 管理所有可呼叫工具，執行緒安全。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
	order []string // 註冊順序，保證列舉確定（測試可重現）
}

// NewRegistry 建立空工具註冊表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register 註冊一個工具；名稱重複時回錯誤（避免靜默覆蓋）。
func (r *Registry) Register(t *Tool) error {
	if t == nil {
		return fmt.Errorf("ai: nil tool")
	}
	if t.Name == "" {
		return fmt.Errorf("ai: tool missing name")
	}
	if t.Run == nil {
		return fmt.Errorf("ai: tool %q missing Run", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("ai: tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// MustRegister 註冊工具，失敗即 panic（僅用於 init/組裝階段，工具為編譯期常量）。
func (r *Registry) MustRegister(t *Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get 依名稱取工具；第二回傳值表示是否存在。
func (r *Registry) Get(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List 依註冊順序回傳所有工具（快照，供 Provider 建 tools schema）。
func (r *Registry) List() []*Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n])
	}
	return out
}

// ToolCallOutcome 單一工具呼叫的結果（並行收集用）。
type ToolCallOutcome struct {
	Call   ToolCall   `json:"call"`
	Result ToolResult `json:"result,omitempty"`
	Err    string     `json:"error,omitempty"`
	// SkippedNeedsConfirm 表示這是寫操作但未帶 confirm，已被 Agent 攔截（未執行）。
	SkippedNeedsConfirm bool `json:"skipped_needs_confirm,omitempty"`
}

// execCall 執行單一工具呼叫。confirm=false 時，寫操作工具不執行 Run。
func (t *Tool) execCall(ctx context.Context, call ToolCall, confirm bool) ToolCallOutcome {
	out := ToolCallOutcome{Call: call}
	if t.RequiresConfirm && !confirm {
		out.SkippedNeedsConfirm = true
		out.Err = "NEEDS_CONFIRM: write operation requires explicit user confirmation"
		return out
	}
	res, err := t.Run(ctx, call.Args)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.Result = res
	return out
}

// ToolCalls 為模型決定的一組工具呼叫。
// 無依賴的多個呼叫以 goroutine 並行執行；同一批次彼此獨立。
// confirm=false 時，寫操作呼叫一律跳過執行（回 NeedsConfirm 語意）。
func (r *Registry) ExecCalls(ctx context.Context, calls []ToolCall, confirm bool) []ToolCallOutcome {
	outcomes := make([]ToolCallOutcome, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		i, call := i, call
		wg.Add(1)
		go func() {
			defer wg.Done()
			tool, ok := r.Get(call.Name)
			if !ok {
				outcomes[i] = ToolCallOutcome{Call: call, Err: fmt.Sprintf("unknown tool: %q", call.Name)}
				return
			}
			outcomes[i] = tool.execCall(ctx, call, confirm)
		}()
	}
	wg.Wait()
	return outcomes
}

// ValidateArgs 依 Parameters JSON Schema 做最小必需參數校驗：
// required 欄位必須存在且型別可接受。回錯誤時模型可換方法。
func ValidateArgs(tool *Tool, args map[string]any) error {
	if tool.Parameters == nil {
		return nil
	}
	req, _ := tool.Parameters["required"].([]any)
	for _, k := range req {
		key, ok := k.(string)
		if !ok {
			continue
		}
		v, present := args[key]
		if !present || v == nil || fmt.Sprintf("%v", v) == "" {
			return fmt.Errorf("tool %q missing required parameter %q", tool.Name, key)
		}
	}
	return nil
}

// sortedKeys 回傳 map 的排序鍵（輸出確定性，供測試與日誌）。
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
