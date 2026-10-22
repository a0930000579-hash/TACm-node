package defi

// flash.go — 閃電貸（Flash Loan）記錄層。
// 主流 DeFi 對標（Aave flashLoan／ERC-3156）：無抵押、同交易償還＋手續費。
// 原子執行（借出→callback→償還）由節點 RPC 層處理；本層只做持久化記錄與查詢。

import (
	"fmt"
	"time"
)

// FlashLoanRecord 閃電貸交易記錄。
type FlashLoanRecord struct {
	ID        int64   `json:"id"`
	Address   string  `json:"address"`
	Asset     string  `json:"asset"`
	Amount    float64 `json:"amount"`
	Fee       float64 `json:"fee"`
	CreatedAt int64   `json:"created_at"`
}

// RecordFlashLoan 記錄一筆閃電貸。
func (s *Store) RecordFlashLoan(address, asset string, amount, fee float64) error {
	if amount <= 0 {
		return fmt.Errorf("閃電貸金額必須大於 0")
	}
	if _, err := s.db.Exec(`INSERT INTO defi_flash_loans(address, asset, amount, fee, created_at) VALUES(?,?,?,?,?)`,
		address, asset, amount, fee, time.Now().Unix()); err != nil {
		return fmt.Errorf("defi: 記錄閃電貸: %w", err)
	}
	return nil
}

// FlashLoanHistory 用戶閃電貸歷史（最新 50 筆，用於前端展示）。
func (s *Store) FlashLoanHistory(address string) ([]FlashLoanRecord, error) {
	rows, err := s.db.Query(`SELECT id, address, asset, amount, fee, created_at FROM defi_flash_loans WHERE address=? ORDER BY id DESC LIMIT 50`, address)
	if err != nil {
		return nil, fmt.Errorf("defi: 查詢閃電貸: %w", err)
	}
	defer rows.Close()
	out := make([]FlashLoanRecord, 0, 8)
	for rows.Next() {
		var r FlashLoanRecord
		if err := rows.Scan(&r.ID, &r.Address, &r.Asset, &r.Amount, &r.Fee, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
