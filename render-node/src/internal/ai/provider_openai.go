package ai

// v166：OpenAI 相容 Provider（function calling）。
// 接標準 {BASE_URL}/chat/completions（messages + tools + tool_calls + role=tool 回餵）。
// env：TAC_LLM_BASE_URL（如 https://api.deepseek.com/v1、https://dashscope.../v1、
//      本地 llama.cpp http://127.0.0.1:8081/v1）、TAC_LLM_KEY、TAC_LLM_MODEL；三者齊備才啟用。
// 選擇順序：OpenAI env > 本地 llama(TAC_LLAMA_PORT) > 離線規則（由路由層組裝 FallbackProvider 鏈）。
// 錯誤（401/429/逾時/壞 JSON）一律回 error，交由 FallbackProvider 降級；絕不把 API key 寫進日誌/回應。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// OpenAIProvider 呼叫 OpenAI 相容 /chat/completions 的 Provider。
type OpenAIProvider struct {
	baseURL string // 已去除結尾斜線，例 https://api.deepseek.com/v1
	apiKey  string
	model   string
	client  *http.Client
}

// NewOpenAIProviderFromEnv 從 env 建立；三者齊備才成功，否則回錯（上層據此降級到下一層）。
// v167：錯誤訊息明確指出缺哪個 env（絕不含 key），方便主網診斷。
func NewOpenAIProviderFromEnv() (*OpenAIProvider, error) {
	base := strings.TrimSpace(os.Getenv("TAC_LLM_BASE_URL"))
	key := strings.TrimSpace(os.Getenv("TAC_LLM_KEY"))
	model := strings.TrimSpace(os.Getenv("TAC_LLM_MODEL"))
	if base == "" || key == "" || model == "" {
		missing := []string{}
		if base == "" {
			missing = append(missing, "TAC_LLM_BASE_URL")
		}
		if key == "" {
			missing = append(missing, "TAC_LLM_KEY")
		}
		if model == "" {
			missing = append(missing, "TAC_LLM_MODEL")
		}
		return nil, fmt.Errorf("openai env incomplete, missing: %s", strings.Join(missing, ", "))
	}
	// 正規化：去結尾斜線；若使用者誤把整個 /chat/completions 貼進 base，剝掉避免路徑重複。
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	return &OpenAIProvider{
		baseURL: base,
		apiKey:  key,
		model:   model,
		client:  &http.Client{Timeout: 90 * time.Second},
	}, nil
}

// Name 回傳提供者名稱。
func (p *OpenAIProvider) Name() string { return "openai" }

// oaiReq OpenAI 相容請求體。
type oaiReq struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Tools       []llamaTool  `json:"tools,omitempty"`
	Temperature float64      `json:"temperature,omitempty"`
	Stream      bool         `json:"stream"`
}

// oaiMessage OpenAI 相容訊息（tool_calls 為 function 結構，arguments 為 JSON 字串）。
type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toOAIMessages 把內部 ChatMessage 轉成 OpenAI 相容格式（tool_calls.arguments 序列化為 JSON 字串）。
func toOAIMessages(msgs []ChatMessage) []oaiMessage {
	out := make([]oaiMessage, 0, len(msgs))
	for _, m := range msgs {
		om := oaiMessage{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			argsStr := ""
			if len(tc.Args) > 0 {
				if b, err := json.Marshal(tc.Args); err == nil {
					argsStr = string(b)
				}
			}
			t := oaiToolCall{ID: tc.ID, Type: "function"}
			t.Function.Name = tc.Name
			t.Function.Arguments = argsStr
			om.ToolCalls = append(om.ToolCalls, t)
		}
		out = append(out, om)
	}
	return out
}

// Complete 實作 Provider：呼叫 /chat/completions，回最終文字或工具呼叫。
func (p *OpenAIProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	lt := make([]llamaTool, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		}
		lt = append(lt, llamaTool{Type: "function", Function: fn})
	}

	body, err := json.Marshal(oaiReq{
		Model:       p.model,
		Messages:    toOAIMessages(msgs),
		Tools:       lt,
		Temperature: 0.3,
		Stream:      false,
	})
	if err != nil {
		return Turn{}, fmt.Errorf("ai: marshal openai req: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Turn{}, fmt.Errorf("ai: new openai req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		// ctx 取消/逾時不餵模型重試。
		if cerr := ctx.Err(); cerr != nil {
			return Turn{}, cerr
		}
		return Turn{}, fmt.Errorf("ai: openai http: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 上限 1MiB，避免誤讀巨大錯誤頁

	// 非 200：依狀態碼回錯（401/429/5xx），不附帶任何金鑰。
	if resp.StatusCode != http.StatusOK {
		return Turn{}, fmt.Errorf("ai: openai HTTP %d", resp.StatusCode)
	}

	var out llamaResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return Turn{}, fmt.Errorf("ai: openai decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return Turn{}, fmt.Errorf("ai: openai empty choices")
	}

	msg := out.Choices[0].Message
	turn := Turn{Answer: strings.TrimSpace(msg.Content)}
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]any{}
			}
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	return turn, nil
}
