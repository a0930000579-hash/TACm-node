package pos

import (
	"errors"
	"fmt"
	"time"
)

// SlashEvent 記錄一次驗證人罰沒（雙簽/缺席等）。
type SlashEvent struct {
	ID        int64   `json:"id"`
	Address   string  `json:"address"`
	Amount    float64 `json:"amount"`
	Rate      float64 `json:"rate"`
	Reason    string  `json:"reason"`
	CreatedAt int64   `json:"created_at"`
}

// JailValidator 監禁驗證人（雙簽/惡意行為）：狀態改 jailed，記錄 jailed_at。
// 監禁期間不參與出塊與投票權重計算。
func (s *Store) JailValidator(address, reason string) (*Validator, error) {
	if address == "" {
		return nil, errors.New("pos: 驗證人地址不能為空")
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE validators SET status='jailed', jailed_at=? WHERE address=? AND status='active'`,
		now, address)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("pos: 驗證人 %s 不存在或已監禁", address)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	v, err := s.byAddress(address)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// UnjailValidator 解除監禁（重新參與共識，需重新質押）。
func (s *Store) UnjailValidator(address string) (*Validator, error) {
	now := time.Now().Unix()
	if _, err := s.db.Exec(
		`UPDATE validators SET status='active', jailed_at=NULL WHERE address=? AND status='jailed'`,
		address); err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(
		`UPDATE pos_params SET value=?, updated_at=? WHERE key='total_validators'`,
		fmt.Sprintf("%d", 0), now); err != nil {
		// 非致命：total_validators 只是統計緩存
	}
	return s.byAddress(address)
}

// SlashValidator 罰沒質押：total/self stake 按 rate 扣減、記 blocks_missed、寫 slash_events。
// rate 以百分比小數傳入（如 0.05 = 5%）。
func (s *Store) SlashValidator(address string, rate float64, reason string) (*SlashEvent, error) {
	if rate <= 0 || rate > 1 {
		return nil, fmt.Errorf("pos: 罰沒率必須在 (0,1]：%g", rate)
	}
	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var total, self float64
	if err := tx.QueryRow(
		`SELECT total_stake, self_stake FROM validators WHERE address=?`, address).
		Scan(&total, &self); err != nil {
		return nil, fmt.Errorf("pos: 驗證人 %s 不存在: %w", address, err)
	}
	amount := total * rate
	newTotal := total - amount
	newSelf := self * (1 - rate)
	if _, err := tx.Exec(
		`UPDATE validators SET total_stake=?, self_stake=?, delegated_stake=?, blocks_missed=blocks_missed+1
		 WHERE address=?`,
		newTotal, newSelf, newTotal-newSelf, address); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT INTO slash_events (address, amount, rate, reason, created_at) VALUES (?,?,?,?,?)`,
		address, amount, rate, reason, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &SlashEvent{Address: address, Amount: amount, Rate: rate, Reason: reason, CreatedAt: now}, nil
}

// GetSlashEvents 返回罰沒事件（新→舊）。
func (s *Store) GetSlashEvents(limit int) ([]SlashEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, address, amount, rate, reason, created_at FROM slash_events
		 ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SlashEvent
	for rows.Next() {
		var e SlashEvent
		if err := rows.Scan(&e.ID, &e.Address, &e.Amount, &e.Rate, &e.Reason, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetActiveValidatorsForConsensus 返回「共識用」活躍驗證人：非監禁且質押 > 0，
// 按總質押降序（權重依質押比例）。
func (s *Store) GetActiveValidatorsForConsensus(count int) ([]Validator, error) {
	if count <= 0 {
		count = s.cfg.ValidatorCount
	}
	rows, err := s.db.Query(
		`SELECT `+validatorCols+` FROM validators
		 WHERE status='active' AND total_stake > 0
		 ORDER BY total_stake DESC LIMIT ?`, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Validator
	for rows.Next() {
		v, err := scanValidator(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// byAddress 依地址查驗證人。
func (s *Store) byAddress(address string) (*Validator, error) {
	return scanValidator(s.db.QueryRow(
		`SELECT `+validatorCols+` FROM validators WHERE address=?`, address))
}
