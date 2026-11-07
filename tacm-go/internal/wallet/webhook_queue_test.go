package wallet

import (
	"testing"
	"time"
)

// TestWebhookQueue 持久化佇列：入列→到期取出→成功刪除。
func TestWebhookQueue(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	id, err := st.EnqueueWebhook([]byte(`{"event":"tiusd_alert"}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}
	if id <= 0 {
		t.Fatalf("id 應 >0, got %d", id)
	}
	jobs, err := st.DueWebhooks(10)
	if err != nil {
		t.Fatalf("DueWebhooks: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("應有 1 筆到期, got %d", len(jobs))
	}
	if err := st.MarkWebhookResult(id, true); err != nil {
		t.Fatalf("MarkWebhookResult(ok): %v", err)
	}
	jobs, _ = st.DueWebhooks(10)
	if len(jobs) != 0 {
		t.Fatalf("成功後佇列應空, got %d", len(jobs))
	}
}

// TestWebhookQueueRetry 失敗退避：attempt 1→1m、2→10m、3→1h；超過上限丟棄。
func TestWebhookQueueRetry(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	id, _ := st.EnqueueWebhook([]byte(`{}`))
	if err := st.MarkWebhookResult(id, false); err != nil {
		t.Fatalf("mark fail #1: %v", err)
	}
	jobs, _ := st.DueWebhooks(10)
	if len(jobs) != 0 {
		t.Fatalf("重試排程中不應到期, got %d", len(jobs))
	}
	// 手動把 next_retry_at 往前移（跳過等待）模擬 4 次失敗 → 超限丟棄
	if _, err := st.db.Exec(`UPDATE webhook_queue SET attempts = 3, next_retry_at = ? WHERE id = ?`, time.Now().Unix()-1, id); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := st.MarkWebhookResult(id, false); err != nil {
		t.Fatalf("mark fail 超限: %v", err)
	}
	jobs, _ = st.DueWebhooks(10)
	if len(jobs) != 0 {
		t.Fatalf("超過重試上限應丟棄, got %d", len(jobs))
	}
}
