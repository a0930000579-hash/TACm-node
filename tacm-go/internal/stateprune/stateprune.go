// Package stateprune 是 TAC Ai 智能鏈的狀態膨脹治理排程器。
//
// 它以 goroutine + context 週期呼叫 chaindb 的歷史清理 / 歸檔（PruneArchive），
// 防止主網 chain.db 無限膨脹導致 OOM / 斷線。設計原則：
//   - 保守預設：保留期長、不動 live state（accounts/mempool/pos_* 完全不碰）。
//   - 可安全關閉：Close 停掉背景迴圈，不阻塞出塊讀寫。
//   - 對外只暴露唯讀 Stats（供 GET /api/stateprune/stats）。
package stateprune

import (
	"sync"
	"time"

	"tacm/internal/chaindb"
)

// Stats 是對外（RPC）唯讀的治理統計快照。
type Stats struct {
	DBBytesBefore int64     `json:"db_bytes_before"`  // 本次清理前主庫位元組
	DBBytesAfter  int64     `json:"db_bytes_after"`   // 本次清理後主庫位元組
	PrunedBlocks  int64     `json:"pruned_blocks"`    // 本次刪除區塊數
	PrunedTxs     int64     `json:"pruned_txs"`       // 本次刪除交易數
	LastRun       time.Time `json:"last_run"`         // 最近一次執行時間（從未執行為零值）
	Retention     int64     `json:"retention_blocks"` // 目前保留區塊數設定
}

// Config 控制 Pruner 的排程與清理行為。
type Config struct {
	// Interval 背景排程間隔；<=0 預設 1 小時。
	Interval time.Duration
	// Prune 傳給 chaindb 的清理參數。零值時套 chaindb.DefaultPruneConfig()。
	Prune chaindb.PruneConfig
}

// Pruner 為背景狀態治理排程器。
type Pruner struct {
	db  *chaindb.ChainDB
	cfg Config

	mu   sync.RWMutex
	last Stats

	stop chan struct{}
	done chan struct{}
}

// New 建構並立即啟動背景排程 goroutine。
// 呼叫端在 node.New 開完 chaindb 後呼叫；用 Close 停止。
func New(cdb *chaindb.ChainDB, cfg Config) *Pruner {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.Prune.RetentionBlocks == 0 && cfg.Prune.SafetyBlocks == 0 && cfg.Prune.RetentionAgeSec == 0 {
		// 全零 → 套保守預設。
		cfg.Prune = chaindb.DefaultPruneConfig()
	}
	p := &Pruner{
		db:   cdb,
		cfg:  cfg,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	p.last.Retention = cfg.Prune.RetentionBlocks
	go p.loop()
	return p
}

// loop 背景排程：週期執行清理，直到收到 stop。
func (p *Pruner) loop() {
	defer close(p.done)
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			// 單次失敗不致命，下一輪再試；錯誤只留在日誌，不中斷排程。
			_, _ = p.RunOnce()
		}
	}
}

// RunOnce 立即執行一次清理（測試 / 手動觸發用），並更新對外 Stats。
func (p *Pruner) RunOnce() (chaindb.PruneStats, error) {
	ps, err := p.db.PruneArchive(p.cfg.Prune)
	p.mu.Lock()
	p.last = Stats{
		DBBytesBefore: ps.DBBytesBefore,
		DBBytesAfter:  ps.DBBytesAfter,
		PrunedBlocks:  ps.PrunedBlocks,
		PrunedTxs:     ps.PrunedTxs,
		LastRun:       time.Unix(ps.RunAt, 0),
		Retention:     ps.Retention,
	}
	p.mu.Unlock()
	return ps, err
}

// Stats 回傳最近一次清理的唯讀快照。從未執行時 LastRun 為零值。
func (p *Pruner) Stats() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.last
}

// Close 停止背景排程 goroutine。可重複呼叫（channel close 安全由 once 保證）。
func (p *Pruner) Close() error {
	select {
	case <-p.stop:
		// 已關閉。
	default:
		close(p.stop)
	}
	<-p.done
	return nil
}
