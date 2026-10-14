package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// M102-4：本地開源大模型整合（llama.cpp llama-server，OpenAI 兼容 API）。
// 優先使用本地 GGUF 模型（Qwen2.5 / TinyLlama 等）；服務不可用時由呼叫端回退規則式助手，
// 確保 AI 助手在任何部署環境都可用。

const (
	// DefaultEndpoint llama-server 預設埠（-web-port/-rpc-port 之外獨立埠）。
	DefaultEndpoint = "http://127.0.0.1:8081"
	healthPath      = "/health"
	chatPath        = "/v1/chat/completions"
)

// ChatMessage OpenAI 兼容訊息。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest llama-server 請求體。
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// ChatChoice 單一回覆選擇。
type ChatChoice struct {
	Message ChatMessage `json:"message"`
}

// ChatResponse llama-server 回應體。
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
}

// LLMClient 本地模型客戶端（llama-server）。
type LLMClient struct {
	Endpoint string
	Client   *http.Client
}

// NewLLMClient 建立客戶端；endpoint 空則用預設。
func NewLLMClient(endpoint string) *LLMClient {
	if endpoint == "" {
		endpoint = DefaultEndpoint
		if p := os.Getenv("TAC_LLAMA_PORT"); p != "" {
			endpoint = "http://127.0.0.1:" + p
		}
	}
	return &LLMClient{
		Endpoint: endpoint,
		Client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Available 偵測 llama-server 是否在線（短超時）。
func (c *LLMClient) Available() bool {
	hc := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := hc.Get(c.Endpoint + healthPath)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Chat 呼叫本地模型，回傳 assistant 回覆內容。messages 須含至少一則訊息。
func (c *LLMClient) Chat(messages []ChatMessage, maxTokens int) (string, error) {
	if len(messages) == 0 {
		return "", fmt.Errorf("ai: 空訊息")
	}
	if maxTokens <= 0 || maxTokens > 1024 {
		maxTokens = 512
	}
	body, err := json.Marshal(ChatRequest{
		Model:       "local",
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: 0.6,
		Stream:      false,
	})
	if err != nil {
		return "", fmt.Errorf("ai: 序列化請求: %w", err)
	}
	resp, err := c.Client.Post(c.Endpoint+chatPath, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ai: 呼叫本地模型: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ai: 本地模型回傳 HTTP %d", resp.StatusCode)
	}
	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ai: 解析回覆: %w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("ai: 模型無回覆")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// SystemPrompt 品牌系統提示詞（中/英）。
func SystemPrompt(lang string) string {
	if lang == "zh" {
		return "你是 FlipOn（TAC AI Smart Chain）的 AI 助手，熟悉區塊鏈、DeFi、錢包、挖礦、交易所與社群功能。" +
			"回答簡潔、準確，以繁體中文為主；不確定時誠實說明。"
	}
	return "You are the FlipOn (TAC AI Smart Chain) AI assistant, knowledgeable about blockchain, DeFi, wallets, " +
		"mining, exchange and community features. Answer concisely and accurately in English; be honest when unsure."
}
