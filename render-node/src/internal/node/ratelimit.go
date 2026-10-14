package node

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// maxRPCPayloadBytes 為單一 RPC 請求體積上限（ABI/source 驗證可能較大，故 2MB）。
const maxRPCPayloadBytes = 2 << 20

// rpcRateReadLimit / rpcRateWriteLimit 為 per-IP 固定視窗限流額度
// （讀取端點 120 次/分；寫入端點 60 次/分，防公開 RPC 被灌水打掛）。
const (
	rpcRateReadLimit  = 120
	rpcRateWriteLimit = 60
)

// ipWindow 記錄某 IP 在當前視窗內的請求計數（讀/寫分離，避免寫入額度被讀取耗盡）。
type ipWindow struct {
	read    int
	write   int
	resetAt time.Time
}

// ipLimiter 為 per-IP 固定視窗限流器（Go 慣用：互斥鎖保護共享 map）。
type ipLimiter struct {
	mu         sync.Mutex
	wins       map[string]*ipWindow
	readLimit  int
	writeLimit int
	window     time.Duration
}

// newIPLimiter 建立限流器；非正數額度回退至安全預設。
func newIPLimiter(readLimit, writeLimit int) *ipLimiter {
	if readLimit <= 0 {
		readLimit = rpcRateReadLimit
	}
	if writeLimit <= 0 {
		writeLimit = rpcRateWriteLimit
	}
	return &ipLimiter{
		wins:       make(map[string]*ipWindow),
		readLimit:  readLimit,
		writeLimit: writeLimit,
		window:     time.Minute,
	}
}

// allow 判定請求是否放行；write=true 時計入並受寫入額度限制。
// 回傳 (允許?, 需等待時長)；拒絕時由調用方回應 429。
func (l *ipLimiter) allow(ip string, write bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w, ok := l.wins[ip]
	if !ok || now.After(w.resetAt) {
		w = &ipWindow{resetAt: now.Add(l.window)}
		l.wins[ip] = w
	}
	if write {
		w.write++
		if w.write > l.writeLimit {
			return false, time.Until(w.resetAt)
		}
		return true, 0
	}
	w.read++
	if w.read > l.readLimit {
		return false, time.Until(w.resetAt)
	}
	return true, 0
}

// cleanup 刪除已過期視窗，避免 map 無界成長；由 limiterCleanup 週期調用。
func (l *ipLimiter) cleanup(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, w := range l.wins {
		if now.After(w.resetAt) {
			delete(l.wins, k)
		}
	}
}

// clientIP 由 RemoteAddr 取出 IP（不含連接埠）。
// 注意：不信任 X-Forwarded-For——若部署在反向代理後，應在代理層做限流或透傳真實 IP。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitWrap 包裝 RPC handler：per-IP 限流（寫入更嚴格）＋請求體積上限。
// 拒絕時回傳 429 + Retry-After，避免公開 RPC 被灌水/超大 body 打掛。
func (s *RPCServer) limitWrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		write := r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete
		ok, wait := s.limiter.allow(clientIP(r), write)
		if !ok {
			retry := int(wait.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error":"rate limit exceeded","retry_after":` + strconv.Itoa(retry) + `}`))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRPCPayloadBytes)
		h(w, r)
	}
}
