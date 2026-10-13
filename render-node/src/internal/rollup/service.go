package rollup

import (
	"context"
	"sync"
	"time"
)

// Service 為 Rollup 後台服務：定時把內存池交易批量打包並提交狀態根到 L1。
// L1 提交失敗（未配置/餘額不足）時塊保留 pending，下一輪自動重試。
type Service struct {
	r      *Rollup
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// StartService 啟動後台服務。
func StartService(r *Rollup) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{r: r, cancel: cancel}
	s.wg.Add(1)
	go s.run(ctx)
	return s
}

func (s *Service) run(ctx context.Context) {
	defer s.wg.Done()
	interval := s.r.cfg.BatchInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick 打包所有可成批的交易並逐批提交到 L1。
func (s *Service) tick() {
	for {
		block, err := s.r.CreateBatch()
		if err != nil || block == nil {
			return
		}
		if _, err := s.r.SubmitToL1(block.Height); err != nil {
			// 提交失敗：塊保持 pending，下一輪重試。
			return
		}
	}
}

// Close 停止服務並等待 goroutine 退出。
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}
