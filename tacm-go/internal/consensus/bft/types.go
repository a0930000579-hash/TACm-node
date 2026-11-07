// Package bft 實現 TAC Ai 智能鏈的 Tendermint 風格拜占庭容錯狀態機，為鏈提供
// 即時最終性：Propose → PreVote(>2/3) → PreCommit(>2/3) → Commit。包含 ECDSA
// 投票簽名、鎖定、view change、雙重投票證據與罰沒；最終化的塊不可回滾。
package bft

// 投票類型。
const (
	Prevote   = "prevote"
	Precommit = "precommit"
)

// 共識步驟。
const (
	StepPropose   = "propose"
	StepPrevote   = "prevote"
	StepPrecommit = "precommit"
	StepCommitted = "committed"
)

// Validator 為驗證人：地址、壓縮公鑰（hex）與投票權重。
type Validator struct {
	Address   string `json:"address"`
	PubkeyHex string `json:"pubkey_hex"`
	Power     int    `json:"power"`
}

// Vote 為一張 BFT 投票。BlockHash 為 nil 表示投 nil。
type Vote struct {
	Type      string  `json:"vote_type"`
	Height    int64   `json:"height"`
	Round     int32   `json:"round"`
	BlockHash *string `json:"block_hash"`
	Validator string  `json:"validator"`
	Signature string  `json:"signature"`
	Ts        int64   `json:"ts"`
}

// SlashingEvidence 為雙重投票（equivocation）罰沒證據。
type SlashingEvidence struct {
	Validator string  `json:"validator"`
	Height    int64   `json:"height"`
	Round     int32   `json:"round"`
	VoteType  string  `json:"vote_type"`
	HashA     *string `json:"hash_a"`
	HashB     *string `json:"hash_b"`
	SigA      string  `json:"sig_a"`
	SigB      string  `json:"sig_b"`
}

// RoundState 為單高度單輪的 BFT 狀態。
type RoundState struct {
	Height int64
	Round  int32
	Step   string

	proposalHash  *string
	proposalValid bool

	lockedHash *string

	// blockHash（用 "":nil 鍵）→ validator → power
	prevotes   map[string]map[string]int
	precommits map[string]map[string]int

	committedHash *string

	sentPrevote   bool
	sentPrecommit bool
}
