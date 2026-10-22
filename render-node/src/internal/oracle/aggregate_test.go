package oracle

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSrc 建立測試用 FuncSource，回傳呼叫次數計數器。
type fakeSrc struct {
	name  string
	w     float64
	px    map[string]float64
	err   error
	calls int
}

func (f *fakeSrc) source() *FuncSource {
	return &FuncSource{
		NameV:   f.name,
		WeightV: f.w,
		FetchFn: func(_ context.Context, symbols []string) (map[string]float64, error) {
			f.calls++
			if f.err != nil {
				return nil, f.err
			}
			out := make(map[string]float64, len(symbols))
			for _, s := range symbols {
				if p, ok := f.px[s]; ok {
					out[s] = p
				}
			}
			return out, nil
		},
	}
}

const tSym = "TACM/USDT"

func newTestAggOracle(band float64, srcs ...*fakeSrc) *Oracle {
	o := New()
	fs := make([]Source, 0, len(srcs))
	for _, s := range srcs {
		fs = append(fs, s.source())
	}
	o.SetAggSources(fs...)
	o.SetAggSymbols([]string{tSym})
	o.SetAggBand(band)
	o.SetAggMinInterval(0) // 預設不節流，測試個別控制
	return o
}

// TestMedianAndWeighted：三源收斂 → 中位數＝加權平均。
func TestMedianAndWeighted(t *testing.T) {
	s1 := &fakeSrc{name: "src-a", w: 1, px: map[string]float64{tSym: 100}}
	s2 := &fakeSrc{name: "src-b", w: 1, px: map[string]float64{tSym: 102}}
	s3 := &fakeSrc{name: "src-c", w: 1, px: map[string]float64{tSym: 104}}
	o := newTestAggOracle(0.10, s1, s2, s3)

	o.ForceRefreshAggregate(context.Background())
	r, ok := o.AggregateDetail(tSym)
	if !ok {
		t.Fatal("應有聚合結果")
	}
	if r.Price < 101.9 || r.Price > 102.1 {
		t.Fatalf("中位數應為 102，got %v", r.Price)
	}
	if r.Weighted < 101.9 || r.Weighted > 102.1 {
		t.Fatalf("加權平均應為 102，got %v", r.Weighted)
	}
	if r.AdoptedN != 3 {
		t.Fatalf("應 3 源全採用，got %d", r.AdoptedN)
	}
	if r.Degraded || r.Stale {
		t.Fatal("不應降級/stale")
	}
	if r.Confidence < 0.9 {
		t.Fatalf("收斂三源置信度應高，got %v", r.Confidence)
	}
}

// TestOutlierRemoval：200 偏離中位數過大 → 標 outlier 剔除，用其餘兩源聚合。
func TestOutlierRemoval(t *testing.T) {
	s1 := &fakeSrc{name: "src-a", w: 1, px: map[string]float64{tSym: 100}}
	s2 := &fakeSrc{name: "src-b", w: 1, px: map[string]float64{tSym: 101}}
	s3 := &fakeSrc{name: "src-bad", w: 1, px: map[string]float64{tSym: 200}}
	o := newTestAggOracle(0.10, s1, s2, s3)

	o.ForceRefreshAggregate(context.Background())
	r, ok := o.AggregateDetail(tSym)
	if !ok {
		t.Fatal("應有聚合結果")
	}
	// 剔除 200 後，採用 {100,101} → 中位數 100.5。
	if r.Price < 100.0 || r.Price > 101.0 {
		t.Fatalf("離群剔除後中位數應約 100.5，got %v", r.Price)
	}
	if r.AdoptedN != 2 {
		t.Fatalf("應僅 2 源採用，got %d", r.AdoptedN)
	}
	var badFound bool
	for _, ss := range r.Sources {
		if ss.Name == "src-bad" {
			badFound = true
			if !ss.Outlier {
				t.Fatal("src-bad 應被標 outlier")
			}
			if ss.Adopted {
				t.Fatal("src-bad 不應被採用")
			}
		}
		if ss.Name == "src-a" && !ss.Adopted {
			t.Fatal("src-a 應被採用")
		}
	}
	if !badFound {
		t.Fatal("聚合詳情應含 src-bad")
	}
}

// TestSingleSourceFailure：一源逾時/錯誤 → 標 stale/error，其餘源仍出價。
func TestSingleSourceFailure(t *testing.T) {
	s1 := &fakeSrc{name: "src-a", w: 1, px: map[string]float64{tSym: 100}}
	s2 := &fakeSrc{name: "src-b", w: 1, px: map[string]float64{tSym: 102}}
	s3 := &fakeSrc{name: "src-down", w: 1, err: errors.New("timeout")}
	o := newTestAggOracle(0.10, s1, s2, s3)

	o.ForceRefreshAggregate(context.Background())
	r, ok := o.AggregateDetail(tSym)
	if !ok {
		t.Fatal("應有聚合結果")
	}
	if r.Price <= 0 {
		t.Fatalf("單源故障不應導致價格歸零，got %v", r.Price)
	}
	if r.AdoptedN != 2 {
		t.Fatalf("應 2 源採用，got %d", r.AdoptedN)
	}
	if r.Degraded {
		t.Fatal("仍有兩源成功，不應降級")
	}
	var down *SourceStatus
	for i := range r.Sources {
		if r.Sources[i].Name == "src-down" {
			down = &r.Sources[i]
		}
	}
	if down == nil {
		t.Fatal("應含 src-down 狀態")
	}
	if !down.Stale || down.Error == "" {
		t.Fatalf("故障源應標 stale+error，got %+v", down)
	}
	if down.Adopted {
		t.Fatal("故障源不應被採用")
	}
}

// TestAllSourcesDegrade：全部源失效 → 保留最後有效價並標 degraded/stale。
func TestAllSourcesDegrade(t *testing.T) {
	good := []*fakeSrc{
		{name: "src-a", w: 1, px: map[string]float64{tSym: 100}},
		{name: "src-b", w: 1, px: map[string]float64{tSym: 102}},
	}
	o := newTestAggOracle(0.10, good[0], good[1])
	o.ForceRefreshAggregate(context.Background())
	r1, _ := o.AggregateDetail(tSym)
	if r1.Price <= 0 || r1.Degraded {
		t.Fatalf("首輪兩源成功應出價，got %+v", r1)
	}

	// 全部源掛掉。
	dead := []*fakeSrc{
		{name: "src-a", w: 1, err: errors.New("net fail")},
		{name: "src-b", w: 1, err: errors.New("net fail")},
	}
	fs := []Source{dead[0].source(), dead[1].source()}
	o.SetAggSources(fs...)
	o.ForceRefreshAggregate(context.Background())
	r2, ok := o.AggregateDetail(tSym)
	if !ok {
		t.Fatal("降級仍應保留結果條目")
	}
	if !r2.Degraded || !r2.Stale {
		t.Fatalf("全源失效應 degraded+stale，got %+v", r2)
	}
	if r2.AdoptedN != 0 {
		t.Fatalf("全源失效應 0 採用，got %d", r2.AdoptedN)
	}
	// 關鍵：保留最後有效價，不歸零。
	if r2.Price <= 0 {
		t.Fatalf("全源失效應保留最後有效價，got %v", r2.Price)
	}
}

// TestThrottle：minInterval 內重複 Refresh 應被節流跳過（fetcher 不重複呼叫）。
func TestThrottle(t *testing.T) {
	c := &fakeSrc{name: "src-a", w: 1, px: map[string]float64{tSym: 100}}
	o := newTestAggOracle(0.10, c)
	o.SetAggMinInterval(10 * time.Second) // 大窗節流

	o.RefreshAggregate(context.Background()) // 第 1 次：實際拉取
	if c.calls != 1 {
		t.Fatalf("首次 Refresh 應拉取 1 次，got %d", c.calls)
	}
	o.RefreshAggregate(context.Background()) // 第 2 次：節流命中，應跳過
	if c.calls != 1 {
		t.Fatalf("節流窗內不應重複拉取，got %d 次呼叫", c.calls)
	}
	// 強制刷新略過節流。
	o.ForceRefreshAggregate(context.Background())
	if c.calls != 2 {
		t.Fatalf("ForceRefresh 應略過節流，got %d 次呼叫", c.calls)
	}
}

// TestSourcesStatus：RPC /sources 視圖正確彙總。
func TestSourcesStatus(t *testing.T) {
	s1 := &fakeSrc{name: "src-a", w: 1, px: map[string]float64{tSym: 100}}
	s2 := &fakeSrc{name: "src-down", w: 1, err: errors.New("boom")}
	o := newTestAggOracle(0.10, s1, s2)
	o.ForceRefreshAggregate(context.Background())

	ss := o.SourcesStatus()
	if len(ss) != 2 {
		t.Fatalf("應有 2 源狀態，got %d", len(ss))
	}
	var down *SourceStatus
	for i := range ss {
		if ss[i].Name == "src-down" {
			down = &ss[i]
		}
	}
	if down == nil || !down.Stale || down.Error == "" {
		t.Fatalf("故障源狀態應 stale+error，got %+v", down)
	}
}
