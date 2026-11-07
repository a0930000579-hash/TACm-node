package web

import (
	"strconv"
	"testing"

	"tacm/internal/chaindb"
	"tacm/internal/node"
)

// countingDS 計算 BlockDetail（昂貴路徑）被呼叫的次數，其餘方法沿用 fakeDS。
type countingDS struct {
	fakeDS
	hash        string
	detailCalls int
}

func (c *countingDS) Block(h int64) (*BlockView, error) {
	return &BlockView{Height: h, Hash: c.hash + strconv.FormatInt(h, 10), Ts: h, Difficulty: 3}, nil
}

func (c *countingDS) BlockHeader(h int64) (*BlockView, error) {
	return c.Block(h)
}

func (c *countingDS) BlockDetail(h int64) (*node.BlockDetail, error) {
	c.detailCalls++
	return &node.BlockDetail{Block: &chaindb.Block{Height: h}}, nil
}

func (c *countingDS) Status() StatusView { return StatusView{Height: 1} }

// TestAuditCacheReusesBlockDetail：同一哈希連續兩輪審計，昂貴的 BlockDetail 只應呼叫一次。
func TestAuditCacheReusesBlockDetail(t *testing.T) {
	ds := &countingDS{hash: "h"}
	cache := newAuditBlockCache()
	auditRecentBlocks(ds, 100, 12, cache)
	auditRecentBlocks(ds, 100, 12, cache)
	if ds.detailCalls != 1 {
		t.Fatalf("同一哈希應只重拉一次 BlockDetail，got %d", ds.detailCalls)
	}
}

// TestAuditCacheInvalidatesOnHashChange：重組使哈希改變 → 快取 miss，重新拉取驗證。
func TestAuditCacheInvalidatesOnHashChange(t *testing.T) {
	ds := &countingDS{hash: "h"}
	cache := newAuditBlockCache()
	auditRecentBlocks(ds, 100, 12, cache)
	ds.hash = "forked"
	auditRecentBlocks(ds, 100, 12, cache)
	if ds.detailCalls != 2 {
		t.Fatalf("哈希改變（重組）應重新 BlockDetail，got %d", ds.detailCalls)
	}
}
