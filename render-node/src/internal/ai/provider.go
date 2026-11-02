package ai

import (
	"context"
	"log"
	"sync"
	"time"
)

// ToolCall 是模型要求 Agent 執行的一次工具呼叫。
type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// Turn 是一輪模型回應：要嘛是最終回答（ToolCalls 為空），要嘛是一組工具呼叫。
type Turn struct {
	Answer    string     `json:"answer,omitempty"`
	Thought   string     `json:"thought,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// HasToolCalls 是否要求呼叫工具。
func (t Turn) HasToolCalls() bool { return len(t.ToolCalls) > 0 }

// Provider 是可插拔的模型提供者。llama（開源 Qwen/DeepSeek 原生 function calling）
// 與 rules（離線規則 fallback，免金鑰可跑可演示）皆實作此介面。
type Provider interface {
	// Complete 給定對話與可用工具，回傳一輪回應。ctx 控制逾時/取消。
	Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error)
	// Name 回傳提供者名稱（llama | rules | test-fake）。
	Name() string
}

// ---------------------------------------------------------------------------
// 對話記憶（sessionID 維護多輪歷史；in-memory + mutex，有上限防無界成長）。
// ---------------------------------------------------------------------------

const (
	memMaxMessages = 40 // 每個 session 最多保留訊息數（超出丟最舊）
	memMaxSessions = 512
)

// MemoryStore 以 sessionID 維護多輪對話歷史，執行緒安全。
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string][]ChatMessage
	order    []string // session 註冊順序，供 LRU 淘汰
}

// NewMemoryStore 建立記憶存放。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string][]ChatMessage)}
}

// Load 回傳某 session 的歷史訊息副本。
func (m *MemoryStore) Load(sessionID string) []ChatMessage {
	if m == nil || sessionID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.sessions[sessionID]
	out := make([]ChatMessage, len(h))
	copy(out, h)
	return out
}

// Append 把訊息寫入 session 歷史，並做上限淘汰。
func (m *MemoryStore) Append(sessionID string, msgs ...ChatMessage) {
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[sessionID]; !ok {
		m.order = append(m.order, sessionID)
		// 超過 session 上限時淘汰最舊 session（FIFO，簡單且夠用）。
		for len(m.order) > memMaxSessions {
			oldest := m.order[0]
			m.order = m.order[1:]
			delete(m.sessions, oldest)
		}
	}
	m.sessions[sessionID] = append(m.sessions[sessionID], msgs...)
	// 單 session 訊息上限：保留尾部 memMaxMessages。
	if n := len(m.sessions[sessionID]); n > memMaxMessages {
		m.sessions[sessionID] = append([]ChatMessage(nil), m.sessions[sessionID][n-memMaxMessages:]...)
	}
}

// Summary 回傳某 session 的近期對話摘要文字（供 read_memory 工具）。
func (m *MemoryStore) Summary(sessionID string) string {
	h := m.Load(sessionID)
	if len(h) == 0 {
		return "（no prior conversation）"
	}
	out := ""
	for _, msg := range h {
		c := msg.Content
		if len(c) > 120 {
			c = c[:120] + "…"
		}
		out += msg.Role + ": " + c + "\n"
	}
	return out
}

// nowTS 回傳當前 Unix 秒（抽出便於測試固定）。
func nowTS() int64 { return time.Now().Unix() }

// FallbackProvider 先嘗試 primary，連續失敗時退回 fallback（llama 不可用→規則式）。
// primary 回錯時，把該錯誤計數並改走 fallback；primary 成功則重置。免金鑰環境永遠可跑。
// v167：降級時把 primary 具體錯誤（HTTP 狀態碼/逾時/解碼錯，絕不含 API key）寫進日誌，方便主網診斷。
type FallbackProvider struct {
	primary  Provider
	fallback Provider
	failures int
	active   Provider // 最近一次成功 Complete 的那一層（讓 Name() 反映實際服務層）
	logger   func(format string, args ...any)
}

// NewFallbackProvider 組裝 primary+fallback。
func NewFallbackProvider(primary, fallback Provider) *FallbackProvider {
	return &FallbackProvider{primary: primary, fallback: fallback}
}

// WithLogger 注入自訂日誌（測試可斷言降級原因）；nil 時用標準 log。
func (f *FallbackProvider) WithLogger(fn func(format string, args ...any)) *FallbackProvider {
	f.logger = fn
	return f
}

func (f *FallbackProvider) logf(format string, args ...any) {
	if f.logger != nil {
		f.logger(format, args...)
		return
	}
	log.Printf("[ai] "+format, args...)
}

// Name 回傳最近一次實際成功服務的提供者名稱（降級後反映真正產生回答的那一層）。
func (f *FallbackProvider) Name() string {
	if f.active != nil {
		return f.active.Name()
	}
	if f.primary != nil && f.failures < 2 {
		return f.primary.Name()
	}
	return f.fallback.Name()
}

// Complete 實作 Provider：primary 失敗逾 2 次後固定走 fallback；成功即記錄 active。
func (f *FallbackProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	if f.primary != nil && f.failures < 2 {
		turn, err := f.primary.Complete(ctx, msgs, tools)
		if err == nil {
			f.failures = 0
			f.active = f.primary
			return turn, nil
		}
		f.failures++
		// v167：明確記錄降級原因（不含 key）。
		f.logf("primary provider %q failed (#%d/2): %v; falling back to %q",
			f.primary.Name(), f.failures, err, f.fallback.Name())
	}
	turn, err := f.fallback.Complete(ctx, msgs, tools)
	if err == nil {
		f.active = f.fallback
	}
	return turn, err
}
