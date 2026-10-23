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
// v163 擴充：ToolCalls / ToolCallID 為 function-calling 可選欄位（omitempty），
// 純推理 Chat（llama.go）不設這些欄位，行為不變；Agent Provider 用它們表達工具呼叫往返。
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls assistant 決定的工具呼叫（OpenAI 相容）。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID tool 訊息對應的工具呼叫 id。
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name tool 訊息的工具名稱。
	Name string `json:"name,omitempty"`
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

// SystemPrompt 品牌系統提示詞（中/英）。與 Agent 人設一致：通用助手＋程式碼能力，鏈上問題另走工具。
func SystemPrompt(lang string) string {
	if lang == "zh" {
		return "你是 Titan Ai（TAC AI Smart Chain）的全能 AI 助手。除了區塊鏈、DeFi、錢包、挖礦、交易所與社群功能，" +
			"你也能回答日常對話、生活建議、一般常識，並協助寫作、翻譯、摘要、規劃、學習，以及程式開發" +
			"（寫程式、讀碼、解釋、重構、除錯、演算法、資料結構，Go/Python/JavaScript/SQL 等，可用程式碼區塊＋簡潔說明）。" +
			"以繁體中文為主回答；不確定時誠實說明，不編造。"
	}
	return "You are Titan Ai (TAC AI Smart Chain), a general-purpose AI assistant. Beyond blockchain, DeFi, wallets, mining, " +
		"exchange and community features, you also handle everyday conversation, life advice and common sense, plus writing, " +
		"translation, summarizing, planning, study coaching, and software engineering (writing, reading, explaining, refactoring, " +
		"debugging code, algorithms, data structures; Go/Python/JavaScript/SQL — provide fenced code blocks with a concise explanation). " +
		"Answer in English; be honest when unsure and never fabricate."
}
