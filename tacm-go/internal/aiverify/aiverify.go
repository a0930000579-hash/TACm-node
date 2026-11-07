package aiverify

// aiverify.go — TAC 鏈上「跨鏈 AI 推理驗證」（對標 Bittensor ROORS / Ritual 驗證網路，簡化版）：
// 驗證者質押 → 跨鏈提交推理結果(input/output CID/hash) → 多名驗證者 attest(approve/reject + 主張 output hash)
// → 達多數/quorum 即定案(verified/rejected) → 正確者分攤任務費獎勵、作惡者沒收部分質押(slashing)。
// 本包只維護純狀態與 SQLite 持久化（aiverify.db）；實際 TACm/TiUSD 資金移轉
// 由 node handler 透過 wallet 服務完成（對標 internal/aimarket 的分工）。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 驗證決議。
const (
	// DecisionApprove 驗證者同意提交的 output hash 正確。
	DecisionApprove = "approve"
	// DecisionReject 驗證者主張提交結果有誤（並提出自己的 claim_hash）。
	DecisionReject = "reject"
)

// 任務狀態機。
const (
	// StatusPending 已提交，等待驗證者投票／挑戰窗口內。
	StatusPending = "pending"
	// StatusVerified 通過多數驗證，結果被認可。
	StatusVerified = "verified"
	// StatusRejected 多數認定提交結果有誤。
	StatusRejected = "rejected"
	// StatusExpired 挑戰窗口結束仍未達 quorum。
	StatusExpired = "expired"
)

// 驗證者質押狀態。
const (
	// VerifierActive 正常在線，可投票。
	VerifierActive = "active"
	// VerifierLocked 質押被凍結（最近被 slash 或進行中），暫停投票。
	VerifierLocked = "locked"
	// VerifierInactive 已提領退場。
	VerifierInactive = "inactive"
)

// 金流帳戶（與 aimarket.FeeAccount 同概念：平台層級帳戶，非個人地址）。
const (
	// StakeAccount 驗證者質押託管帳戶。
	StakeAccount = "aiverify_stake"
	// RewardAccount 任務獎勵池／slash 沒收入帳帳戶。
	RewardAccount = "aiverify_reward"
)

// 可配置閾值（預設值；節點層可在 Open 後用 SetParams 調整）。
const (
	// DefaultMinStake 成為驗證者最低質押（TACm）。
	DefaultMinStake = 10.0
	// DefaultQuorumBps 多數門檻（bps）：5001 = >50%。
	DefaultQuorumBps = 5001
	// DefaultMinVerifiers 成案所需最小驗證者投票數。
	DefaultMinVerifiers = 3
	// DefaultSlashFraction 作惡者沒收質押比例（0.10 = 10%）。
	DefaultSlashFraction = 0.10
	// DefaultChallengeWindowSec 挑戰／投票窗口（秒）。
	DefaultChallengeWindowSec int64 = 3600
)

// Params 驗證網路可調參數。
type Params struct {
	MinStake         float64 `json:"min_stake"`
	QuorumBps        int64   `json:"quorum_bps"`
	MinVerifiers     int64   `json:"min_verifiers"`
	SlashFraction    float64 `json:"slash_fraction"`
	ChallengeWindowS int64   `json:"challenge_window_sec"`
}

// Verifier 驗證者。
type Verifier struct {
	Address      string  `json:"address"`
	Stake        float64 `json:"stake"`
	Status       string  `json:"status"`
	Reputation   int64   `json:"reputation"`    // 投票正確次數
	SlashedCount int64   `json:"slashed_count"` // 被 slash 次數
	Enabled      bool    `json:"enabled"`
	CreatedAt    int64   `json:"created_at"`
	UpdatedAt    int64   `json:"updated_at"`
}

// InferenceTask 跨鏈 AI 推理驗證任務。
type InferenceTask struct {
	Id                 int64   `json:"id"`
	SourceChain        string  `json:"source_chain"`
	ModelId            int64   `json:"model_id"`
	ModelVersion       string  `json:"model_version"`
	InputCID           string  `json:"input_cid"`
	InputHash          string  `json:"input_hash"`
	Submitter          string  `json:"submitter"`
	ExpectedOutputHash string  `json:"expected_output_hash"`
	Status             string  `json:"status"`
	TaskFee            float64 `json:"task_fee"`
	RewardPool         float64 `json:"reward_pool"`
	CreatedAt          int64   `json:"created_at"`
	ChallengeDeadline  int64   `json:"challenge_deadline"`
	SettledAt          int64   `json:"settled_at"`
}

// Attestation 驗證者對任務的投票/證明。
type Attestation struct {
	Id        int64  `json:"id"`
	TaskId    int64  `json:"task_id"`
	Verifier  string `json:"verifier"`
	Decision  string `json:"decision"`
	ClaimHash string `json:"claim_hash"`
	Signature string `json:"signature"`
	CreatedAt int64  `json:"created_at"`
}

// RewardRecord 一名正確驗證者的獎勵分配。
type RewardRecord struct {
	Verifier string  `json:"verifier"`
	Amount   float64 `json:"amount"`
}

// SlashRecord 一名作惡驗證者的沒收明細。
type SlashRecord struct {
	Verifier string  `json:"verifier"`
	Stake    float64 `json:"stake"`
	Slash    float64 `json:"slash"`
}

// SettleResult 一次結算結果（node rpc 據此執行實際金流）。
type SettleResult struct {
	TaskId          int64          `json:"task_id"`
	Status          string         `json:"status"`
	ConsensusHash   string         `json:"consensus_hash"`
	TotalVotes      int            `json:"total_votes"`
	Quorum          int64          `json:"quorum"`
	RewardPool      float64        `json:"reward_pool"`
	Rewards         []RewardRecord `json:"rewards"`
	Slashes         []SlashRecord  `json:"slashes"`
	SubmitterRefund float64        `json:"submitter_refund"`
	SettledAt       int64          `json:"settled_at"`
}

// Store AI 推理驗證持久化。
type Store struct {
	db     *sql.DB
	mu     sync.Mutex
	params Params
	nowFn  func() int64 // 可注入時鐘（測試用）
}

// Open 開啟 aiverify.db（WAL、單連線）並建表。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("aiverify: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "aiverify.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("aiverify: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, nowFn: func() int64 { return time.Now().Unix() }}
	s.params = Params{
		MinStake:         DefaultMinStake,
		QuorumBps:        DefaultQuorumBps,
		MinVerifiers:     DefaultMinVerifiers,
		SlashFraction:    DefaultSlashFraction,
		ChallengeWindowS: DefaultChallengeWindowSec,
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// SetParams 調整驗證網路參數（節點治理可呼叫；小於等 0 則保留原值）。
func (s *Store) SetParams(p Params) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.MinStake > 0 {
		s.params.MinStake = p.MinStake
	}
	if p.QuorumBps > 0 {
		s.params.QuorumBps = p.QuorumBps
	}
	if p.MinVerifiers > 0 {
		s.params.MinVerifiers = p.MinVerifiers
	}
	if p.SlashFraction > 0 {
		s.params.SlashFraction = p.SlashFraction
	}
	if p.ChallengeWindowS > 0 {
		s.params.ChallengeWindowS = p.ChallengeWindowS
	}
}

// GetParams 回傳目前參數（複製）。
func (s *Store) GetParams() Params {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.params
}

// now 回傳目前 Unix 時間（秒）。
func (s *Store) now() int64 { return s.nowFn() }

// norm 正規化地址字串（去空白；實際 0x→tx0 在 node 層做，此處保留原樣）。
func norm(a string) string { return strings.TrimSpace(a) }
