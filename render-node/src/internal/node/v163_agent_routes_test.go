package node

// v163 Agent 端點測試：POST /api/ai/agent 的 happy path、確認閘、error path。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func agentPost(t *testing.T, s *RPCServer, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ai/agent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleAIAgent(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestAIAgent_BalanceHappy(t *testing.T) {
	s := newAIHost(t)
	code, out := agentPost(t, s, `{"message":"what is my balance","address":"`+testAlice+`","session_id":"t1"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%v", code, out)
	}
	if out["ok"] != true {
		t.Fatalf("expected ok, got %v", out)
	}
	answer, _ := out["answer"].(string)
	if strings.TrimSpace(answer) == "" {
		t.Fatalf("expected non-empty answer: %v", out)
	}
	// trace 應含 get_balance 工具呼叫。
	trace, _ := out["trace"].([]any)
	sawBalance := false
	for _, tr := range trace {
		m, _ := tr.(map[string]any)
		if m["tool"] == "get_balance" {
			sawBalance = true
		}
	}
	if !sawBalance {
		t.Fatalf("trace missing get_balance: %v", out["trace"])
	}
}

func TestAIAgent_SwapNeedsConfirm(t *testing.T) {
	s := newAIHost(t)
	// 未帶 confirm → 應停在 needs_confirm，且不執行寫工具。
	code, out := agentPost(t, s, `{"message":"please swap 100 TAC to stablecoin","address":"`+testAlice+`","session_id":"t2"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%v", code, out)
	}
	if out["needs_confirm"] != true {
		t.Fatalf("expected needs_confirm=true, got %v", out)
	}
	// trace 應含 needs_confirm 步驟。
	trace, _ := out["trace"].([]any)
	sawConfirm := false
	for _, tr := range trace {
		m, _ := tr.(map[string]any)
		if m["kind"] == "needs_confirm" {
			sawConfirm = true
		}
	}
	if !sawConfirm {
		t.Fatalf("trace missing needs_confirm step: %v", out["trace"])
	}
}

func TestAIAgent_BadJSON(t *testing.T) {
	s := newAIHost(t)
	code, out := agentPost(t, s, `{not json`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%v", code, out)
	}
}

func TestAIAgent_EmptyMessage(t *testing.T) {
	s := newAIHost(t)
	code, out := agentPost(t, s, `{"message":"   "}`)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%v", code, out)
	}
}

// 確保 bytes import（未來擴充請求緩衝保留）。
var _ = bytes.MinRead
