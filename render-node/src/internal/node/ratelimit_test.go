package node

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestIPLimiterAllow 驗證 per-IP 固定視窗限流：讀取額度內放行、超額拒絕、過期後重置。
func TestIPLimiterAllow(t *testing.T) {
	l := newIPLimiter(3, 1) // 讀 3 次/分、寫 1 次/分
	ip := "127.0.0.1"
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow(ip, false); !ok {
			t.Fatalf("第 %d 次讀取應放行", i+1)
		}
	}
	if ok, _ := l.allow(ip, false); ok {
		t.Fatal("第 4 次讀取應被拒絕")
	}
	// 寫入額度獨立且更嚴格：1 次後拒絕
	if ok, _ := l.allow(ip, true); !ok {
		t.Fatal("首次寫入應放行")
	}
	if ok, _ := l.allow(ip, true); ok {
		t.Fatal("第二次寫入應被拒絕")
	}
	// 不同 IP 不受影響
	if ok, _ := l.allow("10.0.0.2", false); !ok {
		t.Fatal("不同 IP 應獨立計數")
	}
}

// TestIPLimiterCleanup 驗證過期視窗被清理（map 不無界成長）。
func TestIPLimiterCleanup(t *testing.T) {
	l := newIPLimiter(1, 1)
	l.allow("1.1.1.1", false)
	l.allow("2.2.2.2", false)
	if got := len(l.wins); got != 2 {
		t.Fatalf("應有 2 個視窗，實際 %d", got)
	}
	// 手動把視窗改成已過期，再清理
	for _, w := range l.wins {
		w.resetAt = w.resetAt.Add(-2 * l.window)
	}
	l.cleanup(time.Now())
	if got := len(l.wins); got != 0 {
		t.Fatalf("清理後應為 0，實際 %d", got)
	}
}

// TestLimitWrap429 驗證超額請求收到 429 + Retry-After。
func TestLimitWrap429(t *testing.T) {
	s := &RPCServer{limiter: newIPLimiter(1, 1)} // 讀 1 次/分
	h := s.limitWrap(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:54321"

	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次請求應 200，實際 %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h(rec2, req)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("第二次請求應 429，實際 %d", rec2.Code)
	}
	if rec2.Header().Get("Retry-After") == "" {
		t.Fatal("429 應帶 Retry-After")
	}
	if !strings.Contains(rec2.Body.String(), "rate limit exceeded") {
		t.Fatalf("429 body 應含錯誤訊息，實際 %s", rec2.Body.String())
	}
}

// TestMaxBodyRejected 驗證超大 body 被拒絕（http.MaxBytesReader 生效）。
func TestMaxBodyRejected(t *testing.T) {
	s := &RPCServer{limiter: newIPLimiter(10, 10)}
	h := s.limitWrap(func(w http.ResponseWriter, r *http.Request) {
		// 讀取完整 body：超過 MaxBytesReader 上限時 Read 回傳 MaxBytesError → 400。
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	big := strings.Repeat("A", maxRPCPayloadBytes+1024)
	req := httptest.NewRequest(http.MethodPost, "/contract/verify", strings.NewReader(big))
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大 body 應被 MaxBytesReader 拒絕（400），實際 %d", rec.Code)
	}
}
