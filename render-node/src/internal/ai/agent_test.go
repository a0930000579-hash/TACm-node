package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedProvider 依呼叫次數回預設 Turn，供測試驅動 Agent 迴圈。
type scriptedProvider struct {
	calls int
	queue []Turn
	// seenToolResults 記錄每次 complete 看到的最後 tool 訊息（斷言用）。
	seen []string
}

func (s *scriptedProvider) Name() string { return "test-fake" }
func (s *scriptedProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	// 記錄最後一則 tool 訊息內容（若有）。
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" {
			s.seen = append(s.seen, msgs[i].Content)
			break
		}
	}
	i := s.calls
	s.calls++
	if i >= len(s.queue) {
		return Turn{Answer: "done"}, nil
	}
	return s.queue[i], nil
}

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	return r
}

func TestAgent_HappyBalance(t *testing.T) {
	reg := newTestRegistry(t)
	called := false
	reg.MustRegister(&Tool{
		Name:        "get_balance",
		Description: "check balance",
		Parameters:  map[string]any{"required": []any{"address"}},
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			called = true
			return ToolResult{Summary: "1234.5 TAC", Data: map[string]any{"balance": "1234.5"}}, nil
		},
	})
	p := &scriptedProvider{queue: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "get_balance", Args: map[string]any{"address": "0xabc"}}}},
		{Answer: "You hold 1234.5 TAC."},
	}}
	agent, err := NewAgent(p, reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "what is my balance", "s1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected get_balance to be called")
	}
	if !strings.Contains(res.Answer, "1234.5") {
		t.Fatalf("answer missing balance: %q", res.Answer)
	}
	// trace 應含一個 tool step。
	var sawTool bool
	for _, s := range res.Trace {
		if s.Kind == "tool" && s.ToolName == "get_balance" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatalf("trace missing tool step: %+v", res.Trace)
	}
}

func TestAgent_ToolErrorRetriesWithFallback(t *testing.T) {
	reg := newTestRegistry(t)
	attempts := 0
	reg.MustRegister(&Tool{
		Name: "risky_swap",
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			attempts++
			return ToolResult{}, errors.New("insufficient liquidity")
		},
	})
	reg.MustRegister(&Tool{
		Name: "read_memory",
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			return ToolResult{Summary: "no memory"}, nil
		},
	})
	// 第一輪呼叫 risky_swap（失敗）；provider 看到錯誤後第二輪改呼叫 read_memory；第三輪回答。
	p := &scriptedProvider{queue: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "risky_swap", Args: map[string]any{}}}},
		{ToolCalls: []ToolCall{{ID: "2", Name: "read_memory", Args: map[string]any{}}}},
		{Answer: "Swap failed due to liquidity; nothing was sent."},
	}}
	agent, err := NewAgent(p, reg, WithMaxIterations(6))
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Run(context.Background(), "swap please", "s2", false)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("expected risky_swap attempted once, got %d", attempts)
	}
	// 斷言 provider 確實看到了錯誤訊息餵回。
	sawErr := false
	for _, s := range p.seen {
		if strings.Contains(s, "insufficient liquidity") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("provider never saw tool error fed back: %v", p.seen)
	}
}

func TestAgent_ConfirmGateNoTransaction(t *testing.T) {
	reg := newTestRegistry(t)
	executed := false
	reg.MustRegister(&Tool{
		Name:            "dex_swap",
		RequiresConfirm: true,
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			executed = true // 不得被呼叫
			return ToolResult{Summary: "swapped", Data: map[string]any{"tx": "0xdead"}}, nil
		},
	})
	p := &scriptedProvider{queue: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "dex_swap", Args: map[string]any{"amount": "100"}}}},
	}}
	agent, err := NewAgent(p, reg)
	if err != nil {
		t.Fatal(err)
	}
	// 未 confirm。
	res, err := agent.Run(context.Background(), "swap 100 TAC", "s3", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsConfirm {
		t.Fatal("expected NeedsConfirm=true")
	}
	if executed {
		t.Fatal("write tool MUST NOT execute without confirmation (transaction broadcast!)")
	}
	if !strings.Contains(res.Answer, "confirmation") {
		t.Fatalf("answer should ask for confirmation: %q", res.Answer)
	}
}

func TestAgent_ConfirmAllowsExecution(t *testing.T) {
	reg := newTestRegistry(t)
	executed := false
	reg.MustRegister(&Tool{
		Name:            "dex_swap",
		RequiresConfirm: true,
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			executed = true
			return ToolResult{Summary: "swapped", Data: map[string]any{"tx": "0xabc"}}, nil
		},
	})
	p := &scriptedProvider{queue: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "dex_swap", Args: map[string]any{"amount": "100"}}}},
		{Answer: "Swap done, tx 0xabc."},
	}}
	agent, err := NewAgent(p, reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "swap 100 TAC", "s4", true)
	if err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("with confirm=true the write tool should execute")
	}
	if res.NeedsConfirm {
		t.Fatal("should not need confirm after explicit confirmation")
	}
}

func TestAgent_ParallelTools(t *testing.T) {
	reg := newTestRegistry(t)
	done := make(chan string, 2)
	for _, n := range []string{"get_balance", "get_recent_txs"} {
		n := n
		reg.MustRegister(&Tool{
			Name: n,
			Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
				done <- n
				return ToolResult{Summary: n + " ok"}, nil
			},
		})
	}
	p := &scriptedProvider{queue: []Turn{
		{ToolCalls: []ToolCall{
			{ID: "1", Name: "get_balance", Args: map[string]any{"address": "0xabc"}},
			{ID: "2", Name: "get_recent_txs", Args: map[string]any{"address": "0xabc"}},
		}},
		{Answer: "Combined answer."},
	}}
	agent, err := NewAgent(p, reg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "balance and txs", "s5", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 2 {
		t.Fatalf("expected 2 parallel tool executions, got %d", len(done))
	}
	if res.Answer == "" {
		t.Fatal("expected final answer")
	}
}

func TestAgent_MaxIterations(t *testing.T) {
	reg := newTestRegistry(t)
	reg.MustRegister(&Tool{
		Name: "get_balance",
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			return ToolResult{Summary: "1"}, nil
		},
	})
	// 永遠要求呼叫工具、永不給最終回答 → 觸達 maxIter。
	p := &alwaysToolProvider{}
	agent, err := NewAgent(p, reg, WithMaxIterations(3))
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "loop", "s6", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Answer, "limit") {
		t.Fatalf("expected iteration-limit answer, got %q", res.Answer)
	}
}

// alwaysToolProvider 永遠要求呼叫同一個工具（用於測試 maxIter 防無限迴圈）。
type alwaysToolProvider struct{}

func (a *alwaysToolProvider) Name() string { return "always-tool" }
func (a *alwaysToolProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	return Turn{ToolCalls: []ToolCall{{ID: "x", Name: "get_balance", Args: map[string]any{}}}}, nil
}

func TestAgent_ContextCancel(t *testing.T) {
	reg := newTestRegistry(t)
	reg.MustRegister(&Tool{
		Name: "slow",
		Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		},
	})
	p := &scriptedProvider{queue: []Turn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "slow", Args: map[string]any{}}}},
	}}
	agent, err := NewAgent(p, reg, WithTimeout(50*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	_, err = agent.Run(context.Background(), "slow op", "s7", false)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected ctx timeout/cancel, got %v", err)
	}
}

func TestAgent_EmptyMessage(t *testing.T) {
	agent, _ := NewAgent(&scriptedProvider{}, newTestRegistry(t))
	if _, err := agent.Run(context.Background(), "   ", "s8", false); err == nil {
		t.Fatal("expected error on empty message")
	}
}

func TestMemoryStore_CapAndSummary(t *testing.T) {
	m := NewMemoryStore()
	for i := 0; i < memMaxMessages+5; i++ {
		m.Append("s", ChatMessage{Role: "user", Content: "hi"})
	}
	h := m.Load("s")
	if len(h) > memMaxMessages {
		t.Fatalf("memory should cap at %d, got %d", memMaxMessages, len(h))
	}
	if m.Summary("nope") == "" {
		t.Fatal("summary should never be empty string")
	}
}

func TestRulesProvider_IntentRouting(t *testing.T) {
	p := NewRulesProvider("0xdefault")
	cases := []struct {
		msg      string
		toolName string
	}{
		{"what is my balance", "get_balance"},
		{"TAC price", "get_price"},
		{"show recent transactions", "get_recent_txs"},
		{"please swap to stablecoin", "dex_swap"},
		{"deposit TAC", "defi_deposit"},
	}
	for _, c := range cases {
		turn, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: c.msg}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !turn.HasToolCalls() {
			t.Fatalf("msg %q expected tool call, got answer %q", c.msg, turn.Answer)
		}
		if turn.ToolCalls[0].Name != c.toolName {
			t.Fatalf("msg %q: expected tool %q, got %q", c.msg, c.toolName, turn.ToolCalls[0].Name)
		}
	}
}

func TestRegistry_UnknownTool(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(&Tool{Name: "a", Run: func(ctx context.Context, args map[string]any) (ToolResult, error) {
		return ToolResult{Summary: "x"}, nil
	}})
	out := reg.ExecCalls(context.Background(), []ToolCall{{ID: "1", Name: "missing", Args: nil}}, true)
	if out[0].Err == "" {
		t.Fatal("expected unknown-tool error")
	}
}
