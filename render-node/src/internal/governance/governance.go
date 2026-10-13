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

// Governance 治理管理器（Goroutine 安全）。
type Governance struct {
	db       *sql.DB
	mu       sync.RWMutex
	applyFn  ApplyFunc
	baseVals map[string]string // 參數基線（提案未通過前的現值）
	paramDef []Param
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
			{Key: "difficulty", Value: "1", Desc: "基礎 PoW 難度"},
			{Key: "tx_fee_bps", Value: "200", Desc: "TACm 交易手續費基準（萬分比，2%=200）"},
			{Key: "tiusd_floor_bps", Value: "3300", Desc: "TiUSD 流通下限佔 TACM 總供應比例（萬分比）"},
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
	}
	for _, s := range stmts {
		if _, err := g.db.Exec(s); err != nil {
			return fmt.Errorf("governance: 初始化資料表失敗: %w", err)
		}
	}
	return nil
}

// Close 關閉資料庫。
func (g *Governance) Close() error { return g.db.Close() }

// SetApplyFunc 註冊參數執行回調。
func (g *Governance) SetApplyFunc(fn ApplyFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.applyFn = fn
}

// Params 回傳可治理參數現值。
func (g *Governance) Params() []Param {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Param, len(g.paramDef))
	copy(out, g.paramDef)
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
func (g *Governance) Propose(title, desc, ptype, paramKey, paramValue, proposer string, height int64, txHash string) (int64, error) {
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
	g.mu.Lock()
	defer g.mu.Unlock()
	res, err := g.db.Exec(
		`INSERT INTO proposals (title, description, ptype, param_key, param_value, proposer, status, start_height, end_height, created_at, tx_hash)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		title, desc, ptype, paramKey, paramValue, proposer,
		StatusVoting, height, height+VotingWindow, time.Now().Unix(), txHash,
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

// validateParam 校驗可治理參數。
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
	if v <= 0 {
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
// 兩階段執行：先讀完到期提案（關閉 SELECT），再逐一寫入，避免 SQLite 單連線鎖衝突。
func (g *Governance) TallyAndExecute(height int64) []*Proposal {
	g.mu.Lock()
	defer g.mu.Unlock()
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
	var settled []*Proposal
	for _, tr := range rows2 {
		p := tr.p
		total := tr.yesP + tr.noP + tr.abP
		passed := total > 0 && tr.yesP >= total*PassThreshold
		newStatus := StatusRejected
		if passed {
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
		if _, err := g.db.Exec(`UPDATE proposals SET status=? WHERE id=?`, newStatus, p.ID); err != nil {
			continue
		}
		p.Status = newStatus
		settled = append(settled, p)
	}
	return settled
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
