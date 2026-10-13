package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLLMClientChat 以 mock llama-server 驗證 Chat 呼叫與解析。
func TestLLMClientChat(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/chat/completions":
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			gotBody = string(buf)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello from model"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewLLMClient(srv.URL)
	if !c.Available() {
		t.Fatal("Available() 應為 true（mock /health 200）")
	}
	got, err := c.Chat([]ChatMessage{{Role: "user", Content: "hi"}}, 64)
	if err != nil {
		t.Fatalf("Chat 失敗: %v", err)
	}
	if got != "hello from model" {
		t.Fatalf("回覆異常: %q", got)
	}
	var req ChatRequest
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("請求體解析失敗: %v", err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Content != "hi" {
		t.Fatalf("請求體內容異常: %+v", req.Messages)
	}
	if req.Stream {
		t.Fatal("stream 應為 false")
	}
}

// TestLLMClientUnavailable 無 llama-server 時 Available=false、Chat 回錯誤。
func TestLLMClientUnavailable(t *testing.T) {
	c := NewLLMClient("http://127.0.0.1:1") // 不可達
	if c.Available() {
		t.Fatal("不可達端點 Available() 應為 false")
	}
	if _, err := c.Chat([]ChatMessage{{Role: "user", Content: "x"}}, 8); err == nil {
		t.Fatal("不可達端點 Chat 應回錯誤")
	}
}

// TestSystemPrompt 雙語系統提示詞。
func TestSystemPrompt(t *testing.T) {
	zh := SystemPrompt("zh")
	en := SystemPrompt("en")
	if !strings.Contains(zh, "FlipOn") || !strings.Contains(zh, "繁體中文") {
		t.Fatalf("中文提示詞異常: %q", zh)
	}
	if !strings.Contains(en, "English") {
		t.Fatalf("英文提示詞異常: %q", en)
	}
}

// TestChatEmptyMessages 空訊息回錯誤。
func TestChatEmptyMessages(t *testing.T) {
	c := NewLLMClient("http://127.0.0.1:9")
	if _, err := c.Chat(nil, 8); err == nil {
		t.Fatal("空訊息應回錯誤")
	}
}
