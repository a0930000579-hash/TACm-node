package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// LlamaProvider 把既有 LLMClient（llama.cpp llama-server，OpenAI 相容）包成 function-calling Provider。
// 要求後端模型（Qwen2.5 / DeepSeek 等原生 function calling）接受 tools 欄位並回 tool_calls。
// 後端不支援工具呼叫時，模型會在 content 給出自然語言回答，本 provider 據此回 Turn.Answer。
type LlamaProvider struct {
	cli *LLMClient
}

// NewLlamaProvider 建立 provider；cli 為 nil 時回錯（呼叫端先偵測 Available）。
func NewLlamaProvider(cli *LLMClient) (*LlamaProvider, error) {
	if cli == nil {
		return nil, fmt.Errorf("ai: nil llama client")
	}
	return &LlamaProvider{cli: cli}, nil
}

// Name 回傳提供者名稱。
func (p *LlamaProvider) Name() string { return "llama" }

// llamaTool OpenAI function 工具描述。
type llamaTool struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}

// llamaReq function-calling 請求體。
type llamaReq struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []llamaTool   `json:"tools,omitempty"`
}

// llamaToolCallMsg 訊息上的工具呼叫。
type llamaToolCallMsg struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字串
	} `json:"function"`
}

// llamaRespChoice 回應選擇。
type llamaRespChoice struct {
	Message struct {
		Content   string             `json:"content"`
		ToolCalls []llamaToolCallMsg `json:"tool_calls"`
	} `json:"message"`
}

type llamaResp struct {
	Choices []llamaRespChoice `json:"choices"`
}

// Complete 實作 Provider。
func (p *LlamaProvider) Complete(ctx context.Context, msgs []ChatMessage, tools []*Tool) (Turn, error) {
	lt := make([]llamaTool, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		}
		lt = append(lt, llamaTool{Type: "function", Function: fn})
	}
	body, err := json.Marshal(llamaReq{Model: "local", Messages: msgs, Tools: lt})
	if err != nil {
		return Turn{}, fmt.Errorf("ai: marshal llama req: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cli.Endpoint+chatPath, bytes.NewReader(body))
	if err != nil {
		return Turn{}, fmt.Errorf("ai: new llama req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.cli.Client.Do(req)
	if err != nil {
		return Turn{}, fmt.Errorf("ai: call llama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Turn{}, fmt.Errorf("ai: llama HTTP %d", resp.StatusCode)
	}
	var out llamaResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Turn{}, fmt.Errorf("ai: decode llama resp: %w", err)
	}
	if len(out.Choices) == 0 {
		return Turn{}, fmt.Errorf("ai: llama empty choices")
	}
	msg := out.Choices[0].Message
	turn := Turn{Answer: strings.TrimSpace(msg.Content)}
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				// 參數壞掉：仍帶呼叫但 args 為空，由 Agent 校驗回餵模型換方法。
				args = map[string]any{}
			}
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		})
	}
	return turn, nil
}
