package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 測試用假 OpenAI 相容 server；不依賴真外部 API。

func setOpenAIEnv(t *testing.T, base string) {
	t.Helper()
	os.Setenv("TAC_LLM_BASE_URL", base)
	os.Setenv("TAC_LLM_KEY", "test-key-secret")
	os.Setenv("TAC_LLM_MODEL", "test-model")
	t.Cleanup(func() {
		os.Unsetenv("TAC_LLM_BASE_URL")
		os.Unsetenv("TAC_LLM_KEY")
		os.Unsetenv("TAC_LLM_MODEL")
	})
}

func TestOpenAI_NormalCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key-secret" {
			t.Errorf("missing/bearer auth header: %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"你好！很高興認識你。"}}]}`)
	}))
	defer srv.Close()
	setOpenAIEnv(t, srv.URL+"/v1")

	p, err := NewOpenAIProviderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openai" {
		t.Fatalf("name=%s", p.Name())
	}
	turn, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turn.Answer, "你好") || turn.HasToolCalls() {
		t.Fatalf("unexpected turn: %+v", turn)
	}
}

// 模型回 tool_calls；同時驗證 assistant 的內部 ToolCalls 被正確序列化成 OpenAI 格式（arguments 為 JSON 字串）。
func TestOpenAI_ToolCallRoundTrip(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_balance","arguments":"{\"address\":\"0xabc\"}"}}]}}]}`)
	}))
	defer srv.Close()
	setOpenAIEnv(t, srv.URL+"/v1")

	p, _ := MustNewOpenAIProvider(t)

	// 帶一則 assistant（含內部 ToolCalls）＋一則 tool 結果，驗證送出的 OpenAI 訊息格式正確。
	msgs := []ChatMessage{
		{Role: "user", Content: "我的餘額"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "get_balance", Args: map[string]any{"address": "0xabc"}}}},
		{Role: "tool", Name: "get_balance", ToolCallID: "call_1", Content: "balance: 5 TAC"},
	}
	turn, err := p.Complete(context.Background(), msgs, []*Tool{{Name: "get_balance", Description: "d", Parameters: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.HasToolCalls() || turn.ToolCalls[0].Name != "get_balance" {
		t.Fatalf("expected get_balance tool call: %+v", turn)
	}
	if got := turn.ToolCalls[0].Args["address"]; got != "0xabc" {
		t.Fatalf("args address=%v", got)
	}

	// 送出的請求 body 應含 OpenAI function 結構，且 arguments 是字串而非物件。
	body := string(captured)
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"function"`) {
		t.Fatalf("request should contain openai tool_calls/function: %s", body)
	}
	if !strings.Contains(body, `"arguments":"{`) {
		t.Fatalf("arguments should be JSON string, body=%s", body)
	}
	// 送出的 messages 含 tool 角色訊息。
	if !strings.Contains(body, `"role":"tool"`) {
		t.Fatalf("request should contain tool role message: %s", body)
	}
}

func TestOpenAI_ErrorDegradesWithoutLeakingKey(t *testing.T) {
	cases := map[string]int{"401": http.StatusUnauthorized, "429": http.StatusTooManyRequests}
	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				io.WriteString(w, `{"error":{"message":"bad"}}`)
			}))
			setOpenAIEnv(t, srv.URL+"/v1")
			p, _ := MustNewOpenAIProvider(t)
			_, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil)
			if err == nil {
				t.Fatalf("expected error for %s", name)
			}
			if strings.Contains(err.Error(), "test-key-secret") {
				t.Fatalf("error must not leak api key: %v", err)
			}
			srv.Close()
		})
	}

	t.Run("bad_json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{not-json`)
		}))
		setOpenAIEnv(t, srv.URL+"/v1")
		p, _ := MustNewOpenAIProvider(t)
		_, err := p.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil)
		if err == nil {
			t.Fatal("expected decode error")
		}
		srv.Close()
	})
}

func TestOpenAI_EnvIncomplete(t *testing.T) {
	os.Unsetenv("TAC_LLM_BASE_URL")
	os.Unsetenv("TAC_LLM_KEY")
	os.Unsetenv("TAC_LLM_MODEL")
	if _, err := NewOpenAIProviderFromEnv(); err == nil {
		t.Fatal("expected error when env incomplete")
	}
}

// MustNewOpenAIProvider 輔助：env 已由 setOpenAIEnv 設好。
func MustNewOpenAIProvider(t *testing.T) (*OpenAIProvider, error) {
	t.Helper()
	return NewOpenAIProviderFromEnv()
}

// 確保 json 套件有使用（避免 import 報廢）。
var _ = json.Marshal
