package ai

// Effort 推理難度（低/中/高）：影響取樣溫度與可用輸出長度，
// 高難度用更低溫度（更聚焦）與更大 token 上限（更完整推理），屬真實推理參數而非僅計費標籤。
type Effort struct {
	Level       string
	Temperature float64
	MaxTokens   int
}

// ParseEffort 解析難度字串；非法值一律回歸低難度。
func ParseEffort(s string) Effort {
	switch s {
	case "high":
		return Effort{Level: "high", Temperature: 0.2, MaxTokens: 1024}
	case "medium":
		return Effort{Level: "medium", Temperature: 0.35, MaxTokens: 512}
	default:
		return Effort{Level: "low", Temperature: 0.5, MaxTokens: 256}
	}
}

// ProviderEffort 可套用於具體模型 Provider 的功能選項。
type ProviderEffort struct{ Effort Effort }
