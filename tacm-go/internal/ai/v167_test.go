package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// v167：provider 選擇鏈可診斷、降級原因入日誌、不含 API key。

func TestFallback_DegradeLogsReasonNoKey(t *testing.T) {
	var logs []string
	capLog := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	// primary 模擬上游回 HTTP 401（金鑰錯）。
	primary := &errProvider{err: errors.New("ai: openai HTTP 401")}
	f := NewFallbackProvider(primary, &okProvider{name: "rules"}).WithLogger(capLog)

	if _, err := f.Complete(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "401") {
		t.Fatalf("degrade log should contain HTTP status 401, got %q", joined)
	}
	if !strings.Contains(joined, "falling back") {
		t.Fatalf("log should say falling back, got %q", joined)
	}
	if f.Name() != "rules" {
		t.Fatalf("after degrade Name should be rules, got %s", f.Name())
	}
	// 降級仍回 ok（fallback 成功）。
}

func TestOpenAI_EnvMissingNamesMissingKeys(t *testing.T) {
	os.Unsetenv("TAC_LLM_BASE_URL")
	os.Unsetenv("TAC_LLM_KEY")
	os.Unsetenv("TAC_LLM_MODEL")
	_, err := NewOpenAIProviderFromEnv()
	if err == nil {
		t.Fatal("expected error when env incomplete")
	}
	if !strings.Contains(err.Error(), "TAC_LLM_BASE_URL") ||
		!strings.Contains(err.Error(), "TAC_LLM_MODEL") {
		t.Fatalf("error should name missing env vars, got %v", err)
	}
}

// 上游 404（路徑錯，Groq base 常見誤判）→ 降級 rules 且日誌含 404、不含 key。
func TestOpenAI_Upstream404Degrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()
	os.Setenv("TAC_LLM_BASE_URL", srv.URL+"/v1")
	os.Setenv("TAC_LLM_KEY", "secret-key-xyz")
	os.Setenv("TAC_LLM_MODEL", "test-model")
	defer func() {
		os.Unsetenv("TAC_LLM_BASE_URL")
		os.Unsetenv("TAC_LLM_KEY")
		os.Unsetenv("TAC_LLM_MODEL")
	}()

	op, err := NewOpenAIProviderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	capLog := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	f := NewFallbackProvider(op, &okProvider{name: "rules"}).WithLogger(capLog)

	turn, err := f.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Answer != "fallback-answer" {
		t.Fatalf("should degrade to rules answer, got %q", turn.Answer)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "404") {
		t.Fatalf("log should contain 404, got %q", joined)
	}
	if strings.Contains(joined, "secret-key-xyz") {
		t.Fatalf("log must NOT leak api key: %q", joined)
	}
	if f.Name() != "rules" {
		t.Fatalf("after degrade Name should be rules, got %s", f.Name())
	}
}

// v167-brand：系統提示含品牌身份與「不主動說 ChatGPT/OpenAI」指令。
func TestSystemPrompt_BrandIdentity(t *testing.T) {
	for _, want := range []string{"Titan Ai", "BRAND", "ChatGPT", "OpenAI", "GPT-OSS"} {
		if !strings.Contains(systemPromptAgent, want) {
			t.Fatalf("system prompt should mention %q", want)
		}
	}
	// llama SystemPrompt 同步。
	if !strings.Contains(SystemPrompt("zh"), "Titan Ai") ||
		!strings.Contains(SystemPrompt("en"), "Titan Ai") {
		t.Fatal("llama SystemPrompt should keep Titan Ai branding")
	}
}

// 離線規則「你是誰」回答自稱 Titan Ai、不說自己是 ChatGPT。
func TestRules_WhoAnswerBrand(t *testing.T) {
	r := NewRulesProvider("")
	turn, err := r.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "你是誰"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turn.Answer, "Titan Ai") {
		t.Fatalf("rules who answer should say Titan Ai, got %q", turn.Answer)
	}
	if strings.Contains(turn.Answer, "I am ChatGPT") || strings.Contains(turn.Answer, "我是 ChatGPT") {
		t.Fatalf("rules who answer must NOT claim ChatGPT: %q", turn.Answer)
	}
}

// 超時降級：primary 逾時 → FallbackProvider 走規則層，answer 非空、ok。
func TestFallback_TimeoutDegradesNonEmpty(t *testing.T) {
	primary := &errProvider{err: context.DeadlineExceeded}
	f := NewFallbackProvider(primary, NewRulesProvider(""))
	turn, err := f.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "你好"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(turn.Answer) == "" {
		t.Fatal("after timeout degrade, answer must be non-empty")
	}
	if f.Name() != "rules" {
		t.Fatalf("after degrade Name should be rules, got %s", f.Name())
	}
}
