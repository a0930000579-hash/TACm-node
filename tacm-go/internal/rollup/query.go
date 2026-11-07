package rollup

// StatusView 為 L2 狀態概況。
type StatusView struct {
	Layer           string `json:"layer"`
	RollupType      string `json:"rollup_type"`
	BlockHeight     int64  `json:"block_height"`
	MempoolSize     int    `json:"mempool_size"`
	StateRoot       string `json:"state_root"`
	AccountCount    int    `json:"account_count"`
	TotalTxs        int64  `json:"total_txs_processed"`
	ChallengePeriod int64  `json:"challenge_period"`
}

// Status 返回 L2 狀態概況。
func (r *Rollup) Status() (*StatusView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, err := r.tree.ComputeRoot()
	if err != nil {
		return nil, err
	}
	var total int64
	for _, b := range r.blocks {
		total += int64(b.TxCount)
	}
	return &StatusView{
		Layer: "L2", RollupType: "optimistic",
		BlockHeight:     int64(len(r.blocks)),
		MempoolSize:     len(r.mempool),
		StateRoot:       root,
		AccountCount:    r.tree.AccountCount(),
		TotalTxs:        total,
		ChallengePeriod: r.cfg.ChallengePeriod,
	}, nil
}

// Account 查詢 L2 賬戶（不存在返回零值）。
func (r *Rollup) Account(addr string) L2Account {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, _ := r.tree.GetAccount(addr)
	return a
}

// Block 查詢 L2 塊；不存在返回 nil。
func (r *Rollup) Block(height int64) *L2Block {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blocks[height]
}

// Tx 查詢 L2 交易；不存在返回 (nil,nil)。
func (r *Rollup) Tx(hash string) (*L2Transaction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.GetTx(hash)
}
