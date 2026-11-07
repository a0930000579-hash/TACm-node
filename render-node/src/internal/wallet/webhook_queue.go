package wallet

import (
	"fmt"
	"time"
)

// WebhookJob 持久化 Webhook 投遞任務（v114：失敗重試佇列持久化，節點重啟後續投）。
type WebhookJob struct {
	ID          int64  `json:"id"`
	Payload     string `json:"payload"`
	Attempts    int    `json:"attempts"`
	NextRetryAt int64  `json:"next_retry_at"`
	CreatedAt   int64  `json:"created_at"`
}

// EnqueueWebhook 將 Webhook 投遞任務寫入持久化佇列（失敗重啟不丟失）。
func (s *Store) EnqueueWebhook(payload []byte) (int64, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO webhook_queue (payload, attempts, next_retry_at, created_at) VALUES (?, 0, ?, ?)`,
		string(payload), now, now)
	if err != nil {
		return 0, fmt.Errorf("wallet: 寫入 webhook 佇列: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("wallet: webhook 佇列 id: %w", err)
	}
	return id, nil
}

// DueWebhooks 回傳到期可投遞的 Webhook 任務（依建立時間升序）。
func (s *Store) DueWebhooks(limit int) ([]WebhookJob, error) {
	if limit <= 0 {
		limit = 50
	}
	now := time.Now().Unix()
	rows, err := s.db.Query(`SELECT id, payload, attempts, next_retry_at, created_at FROM webhook_queue
		WHERE next_retry_at <= ? ORDER BY id LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("wallet: 查詢 webhook 佇列: %w", err)
	}
	defer rows.Close()
	var out []WebhookJob
	for rows.Next() {
		var j WebhookJob
		if err := rows.Scan(&j.ID, &j.Payload, &j.Attempts, &j.NextRetryAt, &j.CreatedAt); err != nil {
			return nil, fmt.Errorf("wallet: 讀取 webhook 任務: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// webhookRetryDelay 失敗退避：首次失敗(attempt 0)→1m、1→10m、2→1h、≥3 丟棄（回傳 0 表示丟棄）。
func webhookRetryDelay(attempts int) time.Duration {
	switch attempts {
	case 0:
		return 1 * time.Minute
	case 1:
		return 10 * time.Minute
	case 2:
		return 1 * time.Hour
	default:
		return 0
	}
}

// MarkWebhookResult 投遞結果：成功刪除；失敗依退避排下次重試（超過上限丟棄）。
func (s *Store) MarkWebhookResult(id int64, ok bool) error {
	if ok {
		_, err := s.db.Exec(`DELETE FROM webhook_queue WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("wallet: 刪除 webhook 任務: %w", err)
		}
		return nil
	}
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM webhook_queue WHERE id = ?`, id).Scan(&attempts); err != nil {
		return fmt.Errorf("wallet: 讀取 webhook 嘗試次數: %w", err)
	}
	delay := webhookRetryDelay(attempts)
	if delay <= 0 {
		_, err := s.db.Exec(`DELETE FROM webhook_queue WHERE id = ?`, id) // 超過重試上限丟棄
		if err != nil {
			return fmt.Errorf("wallet: 丟棄 webhook 任務: %w", err)
		}
		return nil
	}
	next := time.Now().Add(delay).Unix()
	_, err := s.db.Exec(`UPDATE webhook_queue SET attempts = ?, next_retry_at = ? WHERE id = ?`,
		attempts+1, next, id)
	if err != nil {
		return fmt.Errorf("wallet: 更新 webhook 重試: %w", err)
	}
	return nil
}

// WebhookQueueStats 佇列統計（供前端顯示）。
func (s *Store) WebhookQueueStats() (pending, failed int, err error) {
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM webhook_queue`).Scan(&pending); err != nil {
		return 0, 0, fmt.Errorf("wallet: webhook 佇列統計: %w", err)
	}
	return pending, 0, nil
}
