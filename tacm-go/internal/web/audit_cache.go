package web

import "sync"

// blockCoinbaseAudit 為單一塊的 coinbase/份額審計結果（不可變塊，可按哈希快取）。
type blockCoinbaseAudit struct {
	height       int64
	coinbasePass int
	coinbaseFail int
	poolPass     int
	poolFail     int
	nodePass     int
	nodeFail     int
	splitPass    int
	splitFail    int
	minted       float64
	issues       []string
}

// auditBlockCache 按區塊哈希快取每塊審計結果。已最終化的塊不可變，同一哈希結果
// 可跨 tick 重用；重組使高度上的哈希改變 → 快取自然 miss 重算，語意不變。
type auditBlockCache struct {
	mu sync.Mutex
	m  map[string]blockCoinbaseAudit
}

// auditCacheCap：快取上限，超過則整批清掉（罕見，僅觸發一次全量重審），避免無界增長。
const auditCacheCap = 2000

func newAuditBlockCache() *auditBlockCache {
	return &auditBlockCache{m: make(map[string]blockCoinbaseAudit)}
}

func (c *auditBlockCache) get(hash string) (blockCoinbaseAudit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[hash]
	return r, ok
}

func (c *auditBlockCache) put(hash string, r blockCoinbaseAudit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= auditCacheCap {
		c.m = make(map[string]blockCoinbaseAudit)
	}
	c.m[hash] = r
}
