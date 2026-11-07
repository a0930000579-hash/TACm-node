// Package governance 提供 TAC Ai 智能鏈的鏈上治理能力：
// 提案（Proposal）、投票（Vote）、到期自動統計與參數執行（TallyAndExecute）。
// 每個提案/投票動作同時產生一筆鏈上交易（memo 前綴 GOV:）作為不可篡改審計軌跡。
package governance

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 提案類型。
const (
	TypeParam    = "param"    // 參數調整（出塊時間/難度/手續費/TiUSD 下限…）
	TypeTreasury = "treasury" // 國庫支出（獎勵池撥款）
	TypeMeta     = "meta"     // 治理決議（公告/方向）
)

// 提案狀態。
const (
	StatusVoting   = "voting"
	StatusPassed   = "passed"
	StatusRejected = "rejected"
	StatusExecuted = "executed"
)

// 投票選擇。
const (
	ChoiceYes     = "yes"
	ChoiceNo      = "no"
	ChoiceAbstain = "abstain"
)

// VotingWindow 投票窗口（區塊數）：提案發起後在此高度內可投票。
const VotingWindow = 20

// PassThreshold 通過門檻：贊成權重佔已投票權重比例。
const PassThreshold = 0.667

// 狀態常數（M151 新增 queued：通過後等待時間鎖）。
const (
	StatusQueued = "queued"
)

// GovConfig 治理參數（M151：對標主流鏈——法定人數/時間鎖/提案門檻）。
// 全部欄位 0 值＝維持既有行為（向後相容）。
type GovConfig struct {
	VotingWindow   int64   `json:"voting_window"`    // 投票窗口（區塊）
	PassThreshold  float64 `json:"pass_threshold"`   // 通過門檻（贊成權重比例）
	QuorumPct      float64 `json:"quorum_pct"`       // 法定人數（總供應權重 %；0=不檢查）
	TimelockBlocks int64   `json:"timelock_blocks"`  // 通過後執行延遲（區塊；0=立即）
	ProposerMin    float64 `json:"proposer_min"`     // 提案人最低權重（總供應 %；0=不檢查）
	TotalSupply    float64 `json:"total_supply"`     // 權重分母（TACM）
	TreasuryFeeBps int64   `json:"treasury_fee_bps"` // 手續費入治理金庫比例（萬分比；0=關閉）
}

// Proposal 鏈上治理提案。
type Proposal struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Desc         string `json:"desc"`
	Type         string `json:"type"`
	ParamKey     string `json:"param_key,omitempty"`
	ParamValue   string `json:"param_value,omitempty"`
	Proposer     string `json:"proposer"`
	Status       string `json:"status"`
	VotesYes     int64  `json:"votes_yes"`
	VotesNo      int64  `json:"votes_no"`
	VotesAbstain int64  `json:"votes_abstain"`
	StartHeight  int64  `json:"start_height"`
	EndHeight    int64  `json:"end_height"`
	CreatedAt    int64  `json:"created_at"`
	TxHash       string `json:"tx_hash,omitempty"`
}

// Vote 單筆鏈上投票。
type Vote struct {
	ProposalID int64   `json:"proposal_id"`
	Voter      string  `json:"voter"`
	Power      float64 `json:"power"`
	Choice     string  `json:"choice"`
	Height     int64   `json:"height"`
	TxHash     string  `json:"tx_hash,omitempty"`
}

// Param 可治理參數（現值）。
type Param struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Desc  string `json:"desc"`
}

// ApplyFunc 提案通過後的參數執行回調（由節點註冊，決定如何套用變更）。
type ApplyFunc func(key, value string) error

// Delegation 治理委託紀錄：delegator 把投票權委託給 delegatee。
type Delegation struct {
	Delegator string `json:"delegator"`
	Delegatee string `json:"delegatee"`
	Height    int64  `json:"height"`
	TxHash    string `json:"tx_hash"`
}

// Governance 治理管理器（Goroutine 安全）。
type Governance struct {
	db       *sql.DB
	mu       sync.RWMutex
	applyFn  ApplyFunc
	baseVals map[string]string // 參數基線（提案未通過前的現值）
	paramDef []Param
	cfg      GovConfig // M151：法定人數/時間鎖/提案門檻（0 值＝既有行為）
}

// New 建立治理管理器並初始化資料表。
func New(path string) (*Governance, error) {
	if path == "" {
		return nil, errors.New("governance: 資料庫路徑不能為空")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("governance: 開啟資料庫失敗: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("governance: 資料庫連線失敗: %w", err)
	}
	g := &Governance{
		db: db,
		paramDef: []Param{
			{Key: "block_time", Value: "1", Desc: "目標出塊間隔（秒）"},
			{Key: "difficulty", Value: "1", Desc: "基礎 PoT 難度"},
			{Key: "tx_fee_bps", Value: "200", Desc: "TACm 交易手續費基準（萬分比，2%=200）"},
			{Key: "tiusd_floor_bps", Value: "3300", Desc: "TiUSD 流通下限佔 TACM 總供應比例（萬分比）"},
			// M151a：治理參數可透過鏈上提案修改（0=關閉/既有行為）；現值由 Params() 動態填入。
			{Key: "gov_quorum_pct", Value: "0", Desc: "法定人數（總供應權重 %，0=關閉）"},
			{Key: "gov_timelock_blocks", Value: "0", Desc: "提案通過後執行延遲（區塊，0=立即）"},
			{Key: "gov_proposer_min", Value: "0", Desc: "提案人最低權重（總供應 %，0=關閉）"},
			{Key: "gov_total_supply", Value: "0", Desc: "權重分母總供應量（TACM，0=未設定）"},
			{Key: "treasury_fee_bps", Value: "0", Desc: "手續費入治理金庫比例（萬分比，0=關閉，10000=全額）"},
		},
	}
	if err := g.init(); err != nil {
		db.Close()
		return nil, err
	}
	return g, nil
}

func (g *Governance) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS proposals (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL,
		  description TEXT NOT NULL DEFAULT '',
			ptype TEXT NOT NULL,
			param_key TEXT NOT NULL DEFAULT '',
			param_value TEXT NOT NULL DEFAULT '',
			proposer TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'voting',
			votes_yes INTEGER NOT NULL DEFAULT 0,
			votes_no INTEGER NOT NULL DEFAULT 0,
			votes_abstain INTEGER NOT NULL DEFAULT 0,
			start_height INTEGER NOT NULL,
			end_height INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			tx_hash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS votes (
			proposal_id INTEGER NOT NULL,
			voter TEXT NOT NULL,
			power REAL NOT NULL DEFAULT 0,
			choice TEXT NOT NULL,
			height INTEGER NOT NULL,
			tx_hash TEXT NOT NULL DEFAULT '',
			UNIQUE(proposal_id, voter)
		)`,
		// v129：治理委託投票（delegateVote）——delegator 將投票權委託給 delegatee。
		`CREATE TABLE IF NOT EXISTS delegations (
			delegator TEXT PRIMARY KEY,
			delegatee TEXT NOT NULL,
			height INTEGER NOT NULL,
			tx_hash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS gov_config (
			id INTEGER PRIMARY KEY CHECK(id=1),
			voting_window INTEGER NOT NULL DEFAULT 20,
			pass_threshold REAL NOT NULL DEFAULT 0.667,
			quorum_pct REAL NOT NULL DEFAULT 0,
			timelock_blocks INTEGER NOT NULL DEFAULT 0,
			proposer_min REAL NOT NULL DEFAULT 0,
			total_supply REAL NOT NULL DEFAULT 0,
			treasury_fee_bps INTEGER NOT NULL DEFAULT 0
		)`,
		// 既有 DB 相容：補 treasury_fee_bps 欄位（新表 CREATE 已含 → duplicate column 忽略）。
		`ALTER TABLE gov_config ADD COLUMN treasury_fee_bps INTEGER NOT NULL DEFAULT 0`,
		`INSERT OR IGNORE INTO gov_config (id, voting_window, pass_threshold, quorum_pct, timelock_blocks, proposer_min, total_supply, treasury_fee_bps)
		 VALUES (1, 20, 0.667, 0, 0, 0, 0, 0)`,
	}
	for _, s := range stmts {
		if _, err := g.db.Exec(s); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("governance: 初始化資料表失敗: %w", err)
		}
	}
	// 載入治理參數（M151）。
	if err := g.loadConfig(); err != nil {
		return err
	}
	return nil
}

// loadConfig 讀取 gov_config 至記憶體。
func (g *Governance) loadConfig() error {
	var cfg GovConfig
	err := g.db.QueryRow(
		`SELECT voting_window, pass_threshold, quorum_pct, timelock_blocks, proposer_min, total_supply, treasury_fee_bps
		 FROM gov_config WHERE id=1`,
	).Scan(&cfg.VotingWindow, &cfg.PassThreshold, &cfg.QuorumPct,
		&cfg.TimelockBlocks, &cfg.ProposerMin, &cfg.TotalSupply, &cfg.TreasuryFeeBps)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("governance: 讀取參數失敗: %w", err)
	}
	if cfg.VotingWindow > 0 {
		g.cfg = cfg
	}
	return nil
}

// SetConfig 更新治理參數（0 值欄位維持現值；僅 node 管理面呼叫）。
func (g *Governance) SetConfig(cfg GovConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cur := g.cfg
	if cfg.VotingWindow > 0 {
		cur.VotingWindow = cfg.VotingWindow
	}
	if cfg.PassThreshold > 0 && cfg.PassThreshold <= 1 {
		cur.PassThreshold = cfg.PassThreshold
	}
	if cfg.QuorumPct >= 0 {
		cur.QuorumPct = cfg.QuorumPct
	}
	if cfg.TimelockBlocks >= 0 {
		cur.TimelockBlocks = cfg.TimelockBlocks
	}
	if cfg.ProposerMin >= 0 {
		cur.ProposerMin = cfg.ProposerMin
	}
	if cfg.TotalSupply > 0 {
		cur.TotalSupply = cfg.TotalSupply
	}
	if cfg.TreasuryFeeBps >= 0 {
		cur.TreasuryFeeBps = cfg.TreasuryFeeBps
	}
	if _, err := g.db.Exec(
		`UPDATE gov_config SET voting_window=?, pass_threshold=?, quorum_pct=?, timelock_blocks=?, proposer_min=?, total_supply=?, treasury_fee_bps=? WHERE id=1`,
		cur.VotingWindow, cur.PassThreshold, cur.QuorumPct, cur.TimelockBlocks,
		cur.ProposerMin, cur.TotalSupply, cur.TreasuryFeeBps,
	); err != nil {
		return fmt.Errorf("governance: 寫入參數失敗: %w", err)
	}
	g.cfg = cur
	return nil
}

// Config 回傳治理參數。
func (g *Governance) Config() GovConfig {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg
}

// Close 關閉資料庫。
func (g *Governance) Close() error { return g.db.Close() }

// SetApplyFunc 註冊參數執行回調。
func (g *Governance) SetApplyFunc(fn ApplyFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.applyFn = fn
}

// Params 回傳可治理參數現值（gov_* 參數動態填入 GovConfig 現值）。
func (g *Governance) Params() []Param {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Param, len(g.paramDef))
	copy(out, g.paramDef)
	for i := range out {
		switch out[i].Key {
		case "gov_quorum_pct":
			out[i].Value = strconv.FormatFloat(g.cfg.QuorumPct, 'f', -1, 64)
		case "gov_timelock_blocks":
			out[i].Value = strconv.FormatInt(g.cfg.TimelockBlocks, 10)
		case "gov_proposer_min":
			out[i].Value = strconv.FormatFloat(g.cfg.ProposerMin, 'f', -1, 64)
		case "gov_total_supply":
			out[i].Value = strconv.FormatFloat(g.cfg.TotalSupply, 'f', -1, 64)
		case "treasury_fee_bps":
			out[i].Value = strconv.FormatInt(g.cfg.TreasuryFeeBps, 10)
		}
	}
	return out
}

// Apply 直接套用參數（供節點啟動時讀取已通過決議）。
func (g *Governance) Apply(key, value string) error {
	g.mu.RLock()
	fn := g.applyFn
	g.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(key, value)
}

// Propose 發起提案：校驗參數合法性並寫入。
// proposerPower 為發起人投票權重（M151：達 ProposerMin×TotalSupply 才可提案；0 表示不檢查）。
func (g *Governance) Propose(title, desc, ptype, paramKey, paramValue, proposer string, proposerPower float64, height int64, txHash string) (int64, error) {
	title = strings.TrimSpace(title)
	proposer = strings.TrimSpace(proposer)
	if title == "" {
		return 0, errors.New("governance: 提案標題不能為空")
	}
	if proposer == "" {
		return 0, errors.New("governance: 提案發起人不能為空")
	}
	ptype = strings.TrimSpace(ptype)
	switch ptype {
	case TypeParam, TypeTreasury, TypeMeta:
	default:
		return 0, fmt.Errorf("governance: 未知提案類型 %q", ptype)
	}
	if ptype == TypeParam {
		if err := g.validateParam(paramKey, paramValue); err != nil {
			return 0, err
		}
	}
	// M151c：treasury 提案驗證——收款地址與正數金額（撥款由出塊引擎自獎勵池執行）。
	if ptype == TypeTreasury {
		if strings.TrimSpace(paramKey) == "" {
			return 0, errors.New("governance: treasury 需填收款地址")
		}
		amt, perr := strconv.ParseFloat(strings.TrimSpace(paramValue), 64)
		if perr != nil || amt <= 0 {
			return 0, errors.New("governance: treasury 金額需為正數")
		}
	}
	// M151：提案人最低權重門檻。
	g.mu.RLock()
	minWeight := g.cfg.TotalSupply * g.cfg.ProposerMin / 100
	checkMin := g.cfg.ProposerMin > 0 && g.cfg.TotalSupply > 0
	window := g.cfg.VotingWindow
	g.mu.RUnlock()
	if window <= 0 {
		window = VotingWindow
	}
	if checkMin && proposerPower < minWeight {
		return 0, fmt.Errorf("governance: 提案人權重 %.6f 低於門檻 %.6f", proposerPower, minWeight)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	res, err := g.db.Exec(
		`INSERT INTO proposals (title, description, ptype, param_key, param_value, proposer, status, start_height, end_height, created_at, tx_hash)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		title, desc, ptype, paramKey, paramValue, proposer,
		StatusVoting, height, height+window, time.Now().Unix(), txHash,
	)
	if err != nil {
		return 0, fmt.Errorf("governance: 建立提案失敗: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("governance: 讀取提案 ID 失敗: %w", err)
	}
	return id, nil
}

// validateParam 校驗可治理參數（M151a：gov_* 治理參數允許 0＝關閉）。
func (g *Governance) validateParam(key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return errors.New("governance: 參數鍵不能為空")
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("governance: 參數值必須為數字: %w", err)
	}
	isGovParam := key == "gov_quorum_pct" || key == "gov_timelock_blocks" ||
		key == "gov_proposer_min" || key == "gov_total_supply" || key == "treasury_fee_bps"
	if !isGovParam && v <= 0 {
		return errors.New("governance: 參數值必須大於 0")
	}
	switch key {
	case "block_time":
		if v < 0.5 || v > 60 {
			return errors.New("governance: block_time 需在 0.5~60 秒之間")
		}
	case "difficulty":
		if v < 1 || v > 100 {
			return errors.New("governance: difficulty 需在 1~100 之間")
		}
	case "tx_fee_bps":
		if v < 0 || v > 5000 {
			return errors.New("governance: tx_fee_bps 需在 0~5000（0~50%）之間")
		}
	case "tiusd_floor_bps":
		if v < 1000 || v > 5000 {
			return errors.New("governance: tiusd_floor_bps 需在 1000~5000（10%~50%）之間")
		}
	case "gov_quorum_pct":
		if v < 0 || v > 100 {
			return errors.New("governance: gov_quorum_pct 需在 0~100 之間")
		}
	case "gov_timelock_blocks":
		if v < 0 || v > 1000000 {
			return errors.New("governance: gov_timelock_blocks 需在 0~1000000 之間")
		}
	case "gov_proposer_min":
		if v < 0 || v > 100 {
			return errors.New("governance: gov_proposer_min 需在 0~100 之間")
		}
	case "gov_total_supply":
		if v < 0 {
			return errors.New("governance: gov_total_supply 不能為負")
		}
	case "treasury_fee_bps":
		if v < 0 || v > 10000 {
			return errors.New("governance: treasury_fee_bps 需在 0~10000（0%~100%）之間")
		}
	default:
		return fmt.Errorf("governance: 未知治理參數 %q", key)
	}
	return nil
}

// Vote 投票：同一提案同一投票人僅一次，權重以傳入 power 計。
func (g *Governance) Vote(proposalID int64, voter, choice string, power float64, height int64, txHash string) error {
	voter = strings.TrimSpace(voter)
	if voter == "" {
		return errors.New("governance: 投票人不能為空")
	}
	if power <= 0 {
		return errors.New("governance: 投票權重必須大於 0")
	}
	switch choice {
	case ChoiceYes, ChoiceNo, ChoiceAbstain:
	default:
		return fmt.Errorf("governance: 未知投票選擇 %q", choice)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var status string
	if err := g.db.QueryRow(`SELECT status FROM proposals WHERE id=?`, proposalID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("governance: 提案 %d 不存在", proposalID)
		}
		return fmt.Errorf("governance: 讀取提案狀態失敗: %w", err)
	}
	if status != StatusVoting {
		return fmt.Errorf("governance: 提案 %d 已結束（%s），無法投票", proposalID, status)
	}
	if _, err := g.db.Exec(
		`INSERT OR IGNORE INTO votes (proposal_id, voter, power, choice, height, tx_hash)
		 VALUES (?,?,?,?,?,?)`,
		proposalID, voter, power, choice, height, txHash,
	); err != nil {
		return fmt.Errorf("governance: 寫入投票失敗: %w", err)
	}
	// 更新提案票數聚合（重新統計，保持與 votes 表一致）。
	if err := g.recount(proposalID); err != nil {
		return err
	}
	return nil
}

// recount 依 votes 表重新統計票數。
func (g *Governance) recount(proposalID int64) error {
	var yes, no, abstain int64
	rows, err := g.db.Query(`SELECT choice FROM votes WHERE proposal_id=?`, proposalID)
	if err != nil {
		return fmt.Errorf("governance: 統計票數失敗: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return err
		}
		switch c {
		case ChoiceYes:
			yes++
		case ChoiceNo:
			no++
		default:
			abstain++
		}
	}
	if _, err := g.db.Exec(
		`UPDATE proposals SET votes_yes=?, votes_no=?, votes_abstain=? WHERE id=?`,
		yes, no, abstain, proposalID,
	); err != nil {
		return fmt.Errorf("governance: 更新票數失敗: %w", err)
	}
	return nil
}

// TallyAndExecute 到期統計：區塊高度驅動，每個新區塊呼叫一次。
// 到期且仍在投票中的提案：統計權重，通過門檻＝贊成權重 ≥ 已投票權重 × 2/3。
// 三階段執行：①持鎖讀取到期提案快照並釋鎖 → ②無鎖計算狀態（param 提案的
// applyFn 於此執行，可安全重入 g.mu——Config/SetConfig 等不再與本函數互鎖）→
// ③持鎖寫回狀態，僅寫入成功者計入回傳（供出塊引擎後續處理）。
func (g *Governance) TallyAndExecute(height int64) []*Proposal {
	// ① 持鎖讀取快照後釋鎖。
	g.mu.Lock()
	rows, err := g.db.Query(
		`SELECT p.id, p.title, p.description, p.ptype, p.param_key, p.param_value, p.proposer, p.status,
		        p.votes_yes, p.votes_no, p.votes_abstain, p.start_height, p.end_height, p.created_at, p.tx_hash,
		        COALESCE(SUM(CASE WHEN v.choice='yes' THEN v.power END),0),
		        COALESCE(SUM(CASE WHEN v.choice='no' THEN v.power END),0),
		        COALESCE(SUM(CASE WHEN v.choice='abstain' THEN v.power END),0)
		 FROM proposals p LEFT JOIN votes v ON v.proposal_id=p.id
		 WHERE p.status=? AND p.end_height<=?
		 GROUP BY p.id`,
		StatusVoting, height,
	)
	if err != nil {
		g.mu.Unlock()
		return nil
	}
	type tallyRow struct {
		p              *Proposal
		yesP, noP, abP float64
	}
	var rows2 []tallyRow
	for rows.Next() {
		tr := tallyRow{p: &Proposal{}}
		if err := rows.Scan(&tr.p.ID, &tr.p.Title, &tr.p.Desc, &tr.p.Type, &tr.p.ParamKey, &tr.p.ParamValue,
			&tr.p.Proposer, &tr.p.Status, &tr.p.VotesYes, &tr.p.VotesNo, &tr.p.VotesAbstain,
			&tr.p.StartHeight, &tr.p.EndHeight, &tr.p.CreatedAt, &tr.p.TxHash,
			&tr.yesP, &tr.noP, &tr.abP); err != nil {
			continue
		}
		rows2 = append(rows2, tr)
	}
	rows.Close()
	applyFn := g.applyFn
	timelock := g.cfg.TimelockBlocks
	quorumPct := g.cfg.QuorumPct
	totalSupply := g.cfg.TotalSupply
	g.mu.Unlock()

	// ② 無鎖計算狀態；param 提案的 applyFn 於此執行（鎖已釋放，可安全重入 g.mu）。
	type settle struct {
		p      *Proposal
		status string
	}
	var pending []settle
	for _, tr := range rows2 {
		p := tr.p
		total := tr.yesP + tr.noP + tr.abP
		passed := total > 0 && tr.yesP >= total*PassThreshold
		newStatus := StatusRejected
		if passed {
			// M151：法定人數檢查（QuorumPct>0 且 TotalSupply>0 時啟用）。
			quorumOK := true
			if quorumPct > 0 && totalSupply > 0 {
				quorumOK = total >= totalSupply*quorumPct/100
			}
			if !quorumOK {
				newStatus = StatusRejected
			} else if timelock > 0 || p.Type == TypeTreasury {
				// 時間鎖或 treasury：一律 queued。
				// treasury 由出塊引擎檢查獎勵池餘額後撥款並標記 executed；
				// 餘額不足時保留 queued，供後續區塊重試（不硬執行）。
				newStatus = StatusQueued
			} else {
				newStatus = StatusPassed
				// 參數提案通過：套用參數（執行回調）。
				if p.Type == TypeParam && applyFn != nil {
					if err := applyFn(p.ParamKey, p.ParamValue); err == nil {
						newStatus = StatusExecuted
					}
				} else {
					newStatus = StatusExecuted
				}
			}
		}
		p.Status = newStatus
		pending = append(pending, settle{p, newStatus})
	}

	// ③ 持鎖寫回狀態；僅寫入成功者計入 settled。
	g.mu.Lock()
	var settled []*Proposal
	for _, s := range pending {
		if _, err := g.db.Exec(`UPDATE proposals SET status=? WHERE id=? AND status=?`, s.status, s.p.ID, StatusVoting); err != nil {
			continue
		}
		settled = append(settled, s.p)
	}
	g.mu.Unlock()
	return settled
}

// ExecuteDue 執行已通過時間鎖的提案（M151；node 每區塊呼叫）。
// queued 提案在 end_height + timelock_blocks ≤ height 時套用參數並標記 executed；
// 執行失敗的提案保留 queued 供後續區塊重試。
// 與 TallyAndExecute 同構：①持鎖讀取→釋鎖 → ②無鎖執行 applyFn → ③持鎖寫回，
// 避免 applyFn 重入 g.mu 造成互鎖。
func (g *Governance) ExecuteDue(height int64) []*Proposal {
	// ① 持鎖讀取快照後釋鎖。
	g.mu.Lock()
	timelock := g.cfg.TimelockBlocks
	if timelock <= 0 {
		g.mu.Unlock()
		return nil
	}
	rows, err := g.db.Query(
		`SELECT id, ptype, param_key, param_value FROM proposals
		 WHERE status=? AND end_height + ? <= ?`,
		StatusQueued, timelock, height,
	)
	if err != nil {
		g.mu.Unlock()
		return nil
	}
	type dueRow struct {
		id           int64
		ptype        string
		paramKey     string
		paramValue   string
	}
	var dues []dueRow
	for rows.Next() {
		var d dueRow
		if err := rows.Scan(&d.id, &d.ptype, &d.paramKey, &d.paramValue); err != nil {
			continue
		}
		dues = append(dues, d)
	}
	rows.Close()
	applyFn := g.applyFn
	g.mu.Unlock()

	// ② 無鎖執行 applyFn。
	type due struct {
		id         int64
		ptype      string
		paramKey   string
		paramValue string
		status     string
	}
	var pending []due
	for _, d := range dues {
		// treasury 由出塊引擎撥款後才標記 executed；此處保留 queued 供 PendingTreasury 掃描。
		if d.ptype == TypeTreasury {
			continue
		}
		status := StatusExecuted
		if d.ptype == TypeParam && applyFn != nil {
			if err := applyFn(d.paramKey, d.paramValue); err != nil {
				continue // 執行失敗：保留 queued，下個區塊重試
			}
		}
		pending = append(pending, due{d.id, d.ptype, d.paramKey, d.paramValue, status})
	}

	// ③ 持鎖寫回，僅成功者計入 settled。
	g.mu.Lock()
	var settled []*Proposal
	for _, d := range pending {
		if _, err := g.db.Exec(`UPDATE proposals SET status=? WHERE id=? AND status=?`, d.status, d.id, StatusQueued); err != nil {
			continue
		}
		settled = append(settled, &Proposal{ID: d.id, Type: d.ptype, Status: d.status,
			ParamKey: d.paramKey, ParamValue: d.paramValue})
	}
	g.mu.Unlock()
	return settled
}

// TreasuryExecution 待執行國庫撥款（M151c：獎勵池為資金來源，對標主流鏈 Treasury 架構）。
type TreasuryExecution struct {
	ProposalID int64
	To         string // 收款 tx0 地址
	Amount     string // TACM 數量
}

// PendingTreasury 回傳已到期（end_height + timelock_blocks ≤ height）且狀態 queued 的
// treasury 提案。node 出塊時呼叫：檢查獎勵池餘額足夠即建撥款交易並 MarkTreasuryExecuted。
func (g *Governance) PendingTreasury(height int64) ([]TreasuryExecution, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	timelock := g.cfg.TimelockBlocks
	rows, err := g.db.Query(
		`SELECT id, param_key, param_value FROM proposals
		 WHERE ptype=? AND status=? AND end_height + ? <= ?`,
		TypeTreasury, StatusQueued, timelock, height,
	)
	if err != nil {
		return nil, fmt.Errorf("governance: 查詢待撥款失敗: %w", err)
	}
	defer rows.Close()
	out := []TreasuryExecution{}
	for rows.Next() {
		var id int64
		var to, amt string
		if err := rows.Scan(&id, &to, &amt); err != nil {
			continue
		}
		out = append(out, TreasuryExecution{ProposalID: id, To: to, Amount: amt})
	}
	return out, nil
}

// MarkTreasuryExecuted 標記 treasury 提案已撥款執行（僅 queued → executed）。
func (g *Governance) MarkTreasuryExecuted(proposalID int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := g.db.Exec(`UPDATE proposals SET status=? WHERE id=? AND status=?`,
		StatusExecuted, proposalID, StatusQueued)
	return err
}

// List 回傳全部提案（新→舊）。
func (g *Governance) List() ([]*Proposal, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rows, err := g.db.Query(
		`SELECT id, title, description, ptype, param_key, param_value, proposer, status,
		        votes_yes, votes_no, votes_abstain, start_height, end_height, created_at, tx_hash
		 FROM proposals ORDER BY id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("governance: 讀取提案列表失敗: %w", err)
	}
	defer rows.Close()
	out := make([]*Proposal, 0)
	for rows.Next() {
		p := &Proposal{}
		if err := rows.Scan(&p.ID, &p.Title, &p.Desc, &p.Type, &p.ParamKey, &p.ParamValue,
			&p.Proposer, &p.Status, &p.VotesYes, &p.VotesNo, &p.VotesAbstain,
			&p.StartHeight, &p.EndHeight, &p.CreatedAt, &p.TxHash); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Votes 回傳指定提案的全部投票。
func (g *Governance) Votes(proposalID int64) ([]Vote, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rows, err := g.db.Query(
		`SELECT proposal_id, voter, power, choice, height, tx_hash FROM votes WHERE proposal_id=?`,
		proposalID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Vote, 0)
	for rows.Next() {
		v := Vote{}
		if err := rows.Scan(&v.ProposalID, &v.Voter, &v.Power, &v.Choice, &v.Height, &v.TxHash); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Delegate 設定/改寫委託（delegator → delegatee；覆寫式，與主流 DAO 一致）。
// 委託後 delegator 自身投票權歸 0，改由 delegatee 代理。
func (g *Governance) Delegate(delegator, delegatee string, height int64, txHash string) error {
	delegator = strings.TrimSpace(delegator)
	delegatee = strings.TrimSpace(delegatee)
	if delegator == "" || delegatee == "" {
		return errors.New("governance: 委託人與受託人不能為空")
	}
	if delegator == delegatee {
		return errors.New("governance: 不能委託給自己（請使用 undelegate 解除）")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := g.db.Exec(
		`INSERT INTO delegations (delegator, delegatee, height, tx_hash) VALUES (?,?,?,?)
		 ON CONFLICT(delegator) DO UPDATE SET delegatee=excluded.delegatee, height=excluded.height, tx_hash=excluded.tx_hash`,
		delegator, delegatee, height, txHash,
	)
	if err != nil {
		return fmt.Errorf("governance: 設定委託失敗: %w", err)
	}
	return nil
}

// Undelegate 解除委託（delegator 收回投票權）。
func (g *Governance) Undelegate(delegator string, height int64, txHash string) error {
	delegator = strings.TrimSpace(delegator)
	if delegator == "" {
		return errors.New("governance: 委託人不能為空")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	res, err := g.db.Exec(`DELETE FROM delegations WHERE delegator=?`, delegator)
	if err != nil {
		return fmt.Errorf("governance: 解除委託失敗: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("governance: 該地址目前沒有委託")
	}
	return nil
}

// Delegations 回傳全部委託紀錄（按委託人排序）。
func (g *Governance) Delegations() ([]Delegation, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rows, err := g.db.Query(`SELECT delegator, delegatee, height, tx_hash FROM delegations ORDER BY delegator`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Delegation, 0)
	for rows.Next() {
		d := Delegation{}
		if err := rows.Scan(&d.Delegator, &d.Delegatee, &d.Height, &d.TxHash); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// DelegateeOf 回傳委託人目前的受託人；無委託回空字串。
func (g *Governance) DelegateeOf(delegator string) string {
	delegator = strings.TrimSpace(delegator)
	if delegator == "" {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	var delegatee string
	if err := g.db.QueryRow(`SELECT delegatee FROM delegations WHERE delegator=?`, delegator).Scan(&delegatee); err != nil {
		return ""
	}
	return delegatee
}

// DelegatorsOf 回傳委託給指定受託人的全部委託人（RPC 層計算聚合投票權用）。
func (g *Governance) DelegatorsOf(delegatee string) []string {
	delegatee = strings.TrimSpace(delegatee)
	if delegatee == "" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	rows, err := g.db.Query(`SELECT delegator FROM delegations WHERE delegatee=?`, delegatee)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil
		}
		out = append(out, d)
	}
	return out
}
