package chaindb

import (
	"database/sql"
	"fmt"
)

// M149m 鏈上質押（去中心化驗證人集）：質押交易 memo 前綴與質押池地址（全網固定常量）。
const (
	PosStakeMemo     = "pos:stake"
	PosStakePoolAddr = "pos_stake_pool"
	// M149m-9：PoT 節點註冊——節點免費註冊公鑰（amount=0、fee=0），
	// 註冊後依在線時長（時間權證）取得無質押出塊資格。
	NodeRegMemo = "node:reg"
)

// PosValidator 鏈上驗證人狀態（由質押交易重放累計，任何節點同步同鏈結果一致）。
type PosValidator struct {
	Address    string  `json:"address"`
	TotalStake float64 `json:"total_stake"`
	Pubkey     string  `json:"pubkey"`
	Status     string  `json:"status"`
	FirstSeen  int64   `json:"first_seen"`
	LastActive int64   `json:"last_active"`
}

// NodeRegistry PoT 節點註冊狀態（node:reg 交易重放，任何節點同步同鏈結果一致）。
type NodeRegistry struct {
	Address    string `json:"address"`
	Pubkey     string `json:"pubkey"`
	FirstSeen  int64  `json:"first_seen"`
	LastActive int64  `json:"last_active"`
}

// ApplyNodeReg 註冊/刷新 PoT 節點（first_seen 保持首次、last_active 滾動更新）。
func (c *ChainDB) ApplyNodeReg(address, pubkey string, ts int64) error {
	if address == "" || pubkey == "" {
		return nil
	}
	_, err := c.db.Exec(`INSERT INTO node_registry (address, pubkey, first_seen, last_active)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET
			pubkey = excluded.pubkey,
			last_active = excluded.last_active`,
		address, pubkey, ts, ts)
	if err != nil {
		return fmt.Errorf("chaindb: 註冊 PoT 節點失敗: %w", err)
	}
	return nil
}

// GetNodeRegistry 返回鏈上全部 PoT 節點註冊（任何節點一致）。
func (c *ChainDB) GetNodeRegistry() ([]NodeRegistry, error) {
	rows, err := c.db.Query(`SELECT address, pubkey, first_seen, last_active FROM node_registry`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NodeRegistry, 0)
	for rows.Next() {
		var v NodeRegistry
		if err := rows.Scan(&v.Address, &v.Pubkey, &v.FirstSeen, &v.LastActive); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// applyNodeRegTx 重放 node:reg：寫入/刷新 PoT 節點註冊（同事務，確定性）。
func applyNodeRegTx(tx *sql.Tx, from, pubkey string, ts int64) error {
	if from == "" {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO node_registry (address, pubkey, first_seen, last_active)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET
			pubkey = excluded.pubkey,
			last_active = excluded.last_active`,
		from, pubkey, ts, ts)
	if err != nil {
		return fmt.Errorf("chaindb: 註冊 PoT 節點失敗: %w", err)
	}
	return nil
}

// ApplyPosStake 累計驗證人質押（InsertBlock 重放時呼叫；第一次質押寫入 first_seen）。
func (c *ChainDB) ApplyPosStake(address, pubkey string, amount float64, ts int64) error {
	if amount <= 0 {
		return nil
	}
	_, err := c.db.Exec(`INSERT INTO pos_validators (address, total_stake, pubkey, status, first_seen, last_active)
		VALUES (?, ?, ?, 'active', ?, ?)
		ON CONFLICT(address) DO UPDATE SET
			total_stake = total_stake + excluded.total_stake,
			pubkey = excluded.pubkey,
			last_active = excluded.last_active`,
		address, amount, pubkey, ts, ts)
	if err != nil {
		return fmt.Errorf("chaindb: 累計質押失敗: %w", err)
	}
	return nil
}

// GetPosValidators 返回鏈上全部驗證人狀態（任何節點一致）。
func (c *ChainDB) GetPosValidators() ([]PosValidator, error) {
	rows, err := c.db.Query(`SELECT address, total_stake, pubkey, status, first_seen, last_active FROM pos_validators`)
	if err != nil {
		return nil, fmt.Errorf("chaindb: 讀驗證人失敗: %w", err)
	}
	defer rows.Close()
	var out []PosValidator
	for rows.Next() {
		var v PosValidator
		if err := rows.Scan(&v.Address, &v.TotalStake, &v.Pubkey, &v.Status, &v.FirstSeen, &v.LastActive); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// JailPosValidator 鏈上罰沒：標記驗證人 jailed（不再進入驗證人集）。
func (c *ChainDB) JailPosValidator(address string) error {
	_, err := c.db.Exec(`UPDATE pos_validators SET status = 'jailed' WHERE address = ?`, address)
	if err != nil {
		return fmt.Errorf("chaindb: 監禁驗證人失敗: %w", err)
	}
	return nil
}

// SlashPosValidator 鏈上罰沒：按比例扣減質押（低於門檻一併監禁）。
func (c *ChainDB) SlashPosValidator(address string, rate float64) error {
	_, err := c.db.Exec(`UPDATE pos_validators
		SET total_stake = total_stake * (1 - ?),
		    status = CASE WHEN total_stake * (1 - ?) < 1 THEN 'jailed' ELSE status END
		WHERE address = ?`, rate, rate, address)
	if err != nil {
		return fmt.Errorf("chaindb: 扣減質押失敗: %w", err)
	}
	return nil
}

// M149m-2 節點引導獎勵：新節點首次「創節點」自動獲 1,000 TACm 並直接質押。
// 資金來源：創世社群基金（genesis:community_pool，100,000 TACm）；限量 PosBootstrapLimit 名、
// 每地址僅一次——社群基金餘額與限量雙重保險（50×1,000=50,000，剩餘留給社群互動獎勵）。
const (
	PosBootstrapMemo   = "pos:bootstrap"
	PosBootstrapAmount = 1_000.0
	PosBootstrapLimit  = 50
)

// GetBootstrapCount 返回已領引導獎勵的節點數（限額判斷）。
func (c *ChainDB) GetBootstrapCount() (int, error) {
	var n int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM pos_bootstraps`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("chaindb: 讀引導獎勵計數失敗: %w", err)
	}
	return n, nil
}

// applyPosBootstrapTx 重放節點引導獎勵（InsertBlock 事務內）：從創世社群基金撥付 1,000
// 至新節點錢包，同時累計質押權重（節點直接具備驗證人資格）。限額/單地址/餘額三重檢查。
func applyPosBootstrapTx(tx *sql.Tx, c *ChainDB, from, pubkey string, ts int64) error {
	if from == "" {
		return nil
	}
	// 單地址僅一次。
	var once int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pos_bootstraps WHERE address = ?`, from).Scan(&once); err != nil {
		return fmt.Errorf("chaindb: 檢查引導獎勵: %w", err)
	}
	if once > 0 {
		return fmt.Errorf("chaindb: 該節點已領取過引導獎勵")
	}
	// 限額。
	var cnt int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pos_bootstraps`).Scan(&cnt); err != nil {
		return fmt.Errorf("chaindb: 檢查引導獎勵限額: %w", err)
	}
	if cnt >= PosBootstrapLimit {
		return fmt.Errorf("chaindb: 節點引導獎勵已額滿（限量 %d）", PosBootstrapLimit)
	}
	// 創世社群基金地址（鏈上固定，跨節點一致）。
	var pool string
	if err := tx.QueryRow(`SELECT to_addr FROM transactions WHERE block_height = 0 AND memo LIKE 'genesis:community%' LIMIT 1`).Scan(&pool); err != nil || pool == "" {
		return fmt.Errorf("chaindb: 找不到創世社群基金地址: %v", err)
	}
	// 社群基金餘額必須 ≥ 1,000。
	var bal float64
	if err := tx.QueryRow(`SELECT COALESCE(balance,0) FROM accounts WHERE address = ?`, pool).Scan(&bal); err != nil {
		return fmt.Errorf("chaindb: 讀社群基金餘額: %w", err)
	}
	if bal < PosBootstrapAmount {
		return fmt.Errorf("chaindb: 社群基金餘額不足，無法發放引導獎勵")
	}
	// 撥付：社群基金 → 新節點；同步累計質押。
	if _, err := tx.Exec(`UPDATE accounts SET balance = balance - ? WHERE address = ?`, PosBootstrapAmount, pool); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO accounts (address, balance, nonce, first_seen, last_active)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT(address) DO UPDATE SET balance = balance + excluded.balance`,
		from, PosBootstrapAmount, ts, ts); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO pos_validators (address, total_stake, pubkey, status, first_seen, last_active)
		VALUES (?, ?, ?, 'active', ?, ?)
		ON CONFLICT(address) DO UPDATE SET
			total_stake = total_stake + excluded.total_stake,
			pubkey = excluded.pubkey,
			last_active = excluded.last_active`,
		from, PosBootstrapAmount, pubkey, ts, ts); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO pos_bootstraps (address, ts) VALUES (?, ?)`, from, ts)
	if err != nil {
		return fmt.Errorf("chaindb: 記錄引導獎勵: %w", err)
	}
	return nil
}
