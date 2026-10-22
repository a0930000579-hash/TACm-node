package aimarket

// verify.go — 【附加檔，僅新增不破壞既有 API】
// 「模型呼叫 → 推理 → 驗證 → 結算」的最小掛鉤：從一次 Invocation 把推理結果打包成
// 可提交給跨鏈 AI 推理驗證網路（internal/aiverify）的參數。
// 刻意保持低耦合：本檔不 import aiverify，只產生純資料 VerifyRequest；
// 真正的「提交任務／attest／出獎／slash」由 node rpc（/api/aiverify/infer 等）編排。

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifySourceChain 本鏈來源標籤（跨鏈提交時帶上，供驗證者知道結果來自哪條鏈）。
const VerifySourceChain = "TACm"

// VerifyRequest 一次「呼叫模型後」應提交到 aiverify 驗證網路的推理結果。
type VerifyRequest struct {
	SourceChain  string  `json:"source_chain"`
	ModelId      int64   `json:"model_id"`
	ModelVersion string  `json:"model_version"`
	Caller       string  `json:"caller"`
	InputHash    string  `json:"input_hash"`
	OutputHash   string  `json:"output_hash"`
	TaskFee      float64 `json:"task_fee"`
}

// HashData 對任意字串做 SHA-256，回傳小寫 hex（供 input/output 指紋）。
func HashData(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "0x" + hex.EncodeToString(sum[:])
}

// BuildVerifyRequest 在 invoke 成功後，由節點層呼叫：把本次推理（caller→model→input→output）
// 打包成 aiverify 提交參數。taskFee 為獎勵池（caller 額外付的驗證獎勵，0 表示不送驗證）。
func BuildVerifyRequest(caller string, m Model, input, output string, taskFee float64) VerifyRequest {
	return VerifyRequest{
		SourceChain:  VerifySourceChain,
		ModelId:      m.Id,
		ModelVersion: m.Version,
		Caller:       strings.TrimSpace(caller),
		InputHash:    HashData(input),
		OutputHash:   HashData(output),
		TaskFee:      taskFee,
	}
}
