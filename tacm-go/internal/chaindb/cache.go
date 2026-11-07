package chaindb

import "sync"

// ReadCache 為鏈讀路徑的記憶體快取（熱點：餘額/nonce/交易/帳戶）。
// 寫入（出塊/重組/重建）後整組失效，保證與 SQLite 一致；讀 QPS 由
// 每請求一次 SQL 降為出塊週期內一次 SQL＋記憶體命中。
type ReadCache struct {
	mu      sync.RWMutex
	balance map[string]string
	nonce   map[string]int64
	tx      map[string]*Transaction
	account map[string]*Account
}

func newReadCache() *ReadCache {
	return &ReadCache{
		balance: make(map[string]string),
		nonce:   make(map[string]int64),
		tx:      make(map[string]*Transaction),
		account: make(map[string]*Account),
	}
}

// getBalance 快取讀餘額（ok=false 表示未命中）。
func (c *ReadCache) getBalance(addr string) (string, bool) {
	c.mu.RLock()
	v, ok := c.balance[addr]
	c.mu.RUnlock()
	return v, ok
}

func (c *ReadCache) putBalance(addr, v string) {
	c.mu.Lock()
	c.balance[addr] = v
	c.mu.Unlock()
}

func (c *ReadCache) getNonce(addr string) (int64, bool) {
	c.mu.RLock()
	v, ok := c.nonce[addr]
	c.mu.RUnlock()
	return v, ok
}

func (c *ReadCache) putNonce(addr string, v int64) {
	c.mu.Lock()
	c.nonce[addr] = v
	c.mu.Unlock()
}

// getTx 回交易副本（防外部修改 aliasing）。
func (c *ReadCache) getTx(hash string) (*Transaction, bool) {
	c.mu.RLock()
	t, ok := c.tx[hash]
	c.mu.RUnlock()
	if !ok || t == nil {
		return nil, false
	}
	cp := *t
	return &cp, true
}

func (c *ReadCache) invalidateTx(hash string) {
	c.mu.Lock()
	delete(c.tx, hash)
	c.mu.Unlock()
}

func (c *ReadCache) putTx(hash string, t *Transaction) {
	if t == nil {
		return
	}
	cp := *t
	c.mu.Lock()
	c.tx[hash] = &cp
	c.mu.Unlock()
}

func (c *ReadCache) getAccount(addr string) (*Account, bool) {
	c.mu.RLock()
	a, ok := c.account[addr]
	c.mu.RUnlock()
	if !ok || a == nil {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (c *ReadCache) putAccount(addr string, a *Account) {
	if a == nil {
		return
	}
	cp := *a
	c.mu.Lock()
	c.account[addr] = &cp
	c.mu.Unlock()
}

// clear 全部失效（任何鏈寫入後呼叫，保守保證一致性）。
func (c *ReadCache) clear() {
	c.mu.Lock()
	c.balance = make(map[string]string)
	c.nonce = make(map[string]int64)
	c.tx = make(map[string]*Transaction)
	c.account = make(map[string]*Account)
	c.mu.Unlock()
}
