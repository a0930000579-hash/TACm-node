package pos

import (
	"fmt"
	"strconv"
	"time"
)

const validatorCols = `id, uid, address, name, total_stake, self_stake,
	delegated_stake, commission_rate, status, joined_at, jailed_at,
	uptime, blocks_proposed, blocks_missed, pubkey_hex`

func scanValidator(scanner interface{ Scan(...any) error }) (*Validator, error) {
	var v Validator
	err := scanner.Scan(
		&v.ID, &v.UID, &v.Address, &v.Name, &v.TotalStake, &v.SelfStake,
		&v.DelegatedStake, &v.CommissionRate, &v.Status, &v.JoinedAt, &v.JailedAt,
		&v.Uptime, &v.BlocksProposed, &v.BlocksMissed, &v.PubkeyHex)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// RegisterValidator 註冊成為驗證人。pubkeyHex 為驗證人公鑰（hex，必填——
// BFT 動態驗證人集重建時以公鑰派生地址校驗身分）。
func (s *Store) RegisterValidator(uid int64, address, name string, commission float64, pubkeyHex string) (*Validator, error) {
	if commission < 0 || commission > 0.2 {
		return nil, fmt.Errorf("傭金率必須在 0-20%% 之間")
	}
	if pubkeyHex == "" {
		return nil, fmt.Errorf("驗證人必須提供公鑰（pubkey_hex）")
	}
	now := time.Now().Unix()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM validators WHERE address=?`, address).
		Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, fmt.Errorf("該地址已註冊為驗證人")
	}

	res, err := tx.Exec(
		`INSERT INTO validators (uid,address,name,commission_rate,status,joined_at,pubkey_hex)
		 VALUES (?,?,?,?,'active',?,?)`,
		uid, address, name, commission, now, pubkeyHex)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	var tv int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM validators`).Scan(&tv); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO pos_params (key,value,updated_at) VALUES (?,?,?)`,
		"total_validators", strconv.Itoa(tv), now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetValidatorInfo(id)
}

// GetValidatorByAddress 依地址返回驗證人（含監禁狀態；M87 RPC/測試用）。
func (s *Store) GetValidatorByAddress(address string) (*Validator, error) {
	return scanValidator(s.db.QueryRow(
		`SELECT `+validatorCols+` FROM validators WHERE address=?`, address))
}

// GetValidatorInfo 返回驗證人詳情。
func (s *Store) GetValidatorInfo(id int64) (*Validator, error) {
	return scanValidator(s.db.QueryRow(
		`SELECT `+validatorCols+` FROM validators WHERE id=?`, id))
}

// GetValidators 返回活躍驗證人（按總質押降序）。
func (s *Store) GetValidators(limit int) ([]Validator, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT `+validatorCols+` FROM validators WHERE status='active'
		 ORDER BY total_stake DESC LIMIT ?`, limit)
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

// GetTopValidators 返回前 N 名驗證人（當前驗證人集合）。
func (s *Store) GetTopValidators(count int) ([]Validator, error) {
	if count <= 0 {
		count = s.cfg.ValidatorCount
	}
	return s.GetValidators(count)
}
