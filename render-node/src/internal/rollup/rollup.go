package rollup

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"tacm/internal/crypto"
)

// Rollup 為 Layer2 Optimistic Rollup 引擎。
type Rollup struct {
	cfg     Config
	store   *Store
	tree    *StateTree
	mu      sync.Mutex
	mempool []*L2Transaction
	blocks  map[int64]*L2Block
	l1      L1Submitter
}

// New 建立 Rollup 引擎並從持久層恢復賬戶、塊與待處理交易。
func New(cfg Config) (*Rollup, error) {
	if cfg.MaxTxsPerBatch <= 0 {
		cfg.MaxTxsPerBatch = 1000
	}
	if cfg.ChallengePeriod <= 0 {
		cfg.ChallengePeriod = 604800
	}
	store, err := OpenStore(filepath.Join(cfg.DataDir, "tac_layer2.db"))
	if err != nil {
		return nil, err
	}
	r := &Rollup{
		cfg: cfg, store: store,
		tree:   NewStateTree(),
		blocks: map[int64]*L2Block{},
	}
	if err := r.load(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return r, nil
}

func (r *Rollup) load() error {
	accs, err := r.store.LoadAccounts()
	if err != nil {
		return err
	}
	for _, a := range accs {
		r.tree.SetAccount(a)
	}
	blks, err := r.store.LoadBlocks()
	if err != nil {
		return err
	}
	for i := range blks {
		b := blks[i]
		r.blocks[b.Height] = &b
	}
	pending, err := r.store.LoadPendingTxs()
	if err != nil {
		return err
	}
	for i := range pending {
		t := pending[i]
		r.mempool = append(r.mempool, &t)
	}
	return nil
}

// SetL1 注入 L1 提交器。
func (r *Rollup) SetL1(s L1Submitter) {
	r.mu.Lock()
	r.l1 = s
	r.mu.Unlock()
}

// Close 關閉持久層。
func (r *Rollup) Close() error { return r.store.Close() }

// SubmitTransaction 驗證並提交 L2 交易（真實簽名、nonce、餘額），返回交易哈希。
func (r *Rollup) SubmitTransaction(tx map[string]any) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	required := []string{"from", "to", "amount", "fee", "nonce", "ts", "signature", "pubkey"}
	for _, f := range required {
		v, ok := tx[f]
		if !ok || v == nil {
			return "", fmt.Errorf("missing field: %s", f)
		}
	}
	from := getString(tx, "from")
	to := getString(tx, "to")
	signature := getString(tx, "signature")

	if !crypto.IsValidAddress(from) {
		return "", fmt.Errorf("invalid from address")
	}
	if !crypto.IsValidAddress(to) {
		return "", fmt.Errorf("invalid to address")
	}
	if !crypto.VerifyTransactionSignature(tx, signature, from) {
		return "", fmt.Errorf("invalid signature")
	}

	amount, err := toFloat(tx, "amount")
	if err != nil {
		return "", fmt.Errorf("invalid amount")
	}
	fee, err := toFloat(tx, "fee")
	if err != nil {
		return "", fmt.Errorf("invalid fee")
	}
	nonce := getInt64(tx, "nonce")
	ts := getInt64(tx, "ts")

	sender, _ := r.tree.GetAccount(from)
	if nonce != sender.Nonce {
		return "", fmt.Errorf("bad nonce: expected %d, got %d", sender.Nonce, nonce)
	}
	if sender.Balance < amount+fee {
		return "", fmt.Errorf("insufficient L2 balance: have %g, need %g",
			sender.Balance, amount+fee)
	}

	// 交易哈希（與 L1 同構：DoubleSHA256(Canonical({p,sig}))）。
	p := map[string]any{
		"from": from, "to": to,
		"amount": getString(tx, "amount"), "fee": getString(tx, "fee"),
		"nonce": nonce, "ts": ts,
	}
	raw, err := crypto.Canonical(map[string]any{"p": p, "sig": signature})
	if err != nil {
		return "", fmt.Errorf("計算交易哈希失敗: %w", err)
	}
	txHash := hex.EncodeToString(crypto.DoubleSHA256(raw))

	l2tx := &L2Transaction{
		TxHash: txHash, FromAddr: from, ToAddr: to,
		Amount: amount, Fee: fee, Nonce: nonce, Ts: ts,
		Signature: signature, Pubkey: getString(tx, "pubkey"),
		Status: StatusPending,
	}
	if err := r.store.SaveTx(*l2tx); err != nil {
		return "", err
	}
	r.mempool = append(r.mempool, l2tx)
	return txHash, nil
}

// CreateBatch 將內存池交易按費用排序、批量執行並打包成 L2 塊；
// 內存池空返回 (nil,nil)。
func (r *Rollup) CreateBatch() (*L2Block, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.mempool) == 0 {
		return nil, nil
	}

	// 按 fee 降序，取上限。
	candidates := make([]*L2Transaction, len(r.mempool))
	copy(candidates, r.mempool)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Fee > candidates[j].Fee
	})
	max := r.cfg.MaxTxsPerBatch
	if len(candidates) > max {
		candidates = candidates[:max]
	}

	prevRoot, err := r.tree.ComputeRoot()
	if err != nil {
		return nil, err
	}
	snap := r.tree.Snapshot()

	executed := make([]*L2Transaction, 0, len(candidates))
	for _, tx := range candidates {
		if applyOnTree(r.tree, tx) {
			tx.Status = StatusConfirmed
			tx.L2Block = int64(len(r.blocks)) + 1
			if err := r.store.SaveTx(*tx); err != nil {
				return nil, err
			}
			executed = append(executed, tx)
		}
	}
	if len(executed) == 0 {
		return nil, nil
	}

	newRoot, err := r.tree.ComputeRoot()
	if err != nil {
		return nil, err
	}
	height := int64(len(r.blocks)) + 1
	txHashes := make([]string, len(executed))
	for i, tx := range executed {
		txHashes[i] = tx.TxHash
	}
	block := &L2Block{
		Height: height, BatchIndex: height,
		TxCount:       len(executed),
		TxHashes:      txHashes,
		StateRoot:     newRoot,
		PrevStateRoot: prevRoot,
		Proposer:      r.proposerLabel(),
		Timestamp:     time.Now().Unix(),
		Status:        BlockPending,
		PreSnapshot:   snap,
	}
	if err := r.store.SaveBlock(*block); err != nil {
		return nil, err
	}
	r.blocks[height] = block

	done := map[string]bool{}
	for _, tx := range executed {
		done[tx.TxHash] = true
	}
	kept := r.mempool[:0]
	for _, tx := range r.mempool {
		if !done[tx.TxHash] {
			kept = append(kept, tx)
		}
	}
	r.mempool = kept
	return block, nil
}

func (r *Rollup) proposerLabel() string {
	if r.cfg.ProposerWIF != "" {
		if kp, err := parseSignerKey(r.cfg.ProposerWIF); err == nil {
			if a, err := kp.Address(); err == nil {
				return a
			}
		}
	}
	return "l2-proposer"
}

// SubmissionView 為 L1 提交結果。
type SubmissionView struct {
	OK                bool   `json:"ok"`
	L2Height          int64  `json:"l2_height"`
	L1TxHash          string `json:"l1_tx_hash"`
	StateRoot         string `json:"state_root"`
	ChallengeDeadline int64  `json:"challenge_deadline"`
}

// SubmitToL1 把指定 L2 塊的狀態根提交到 Layer1，進入挑戰期。
func (r *Rollup) SubmitToL1(height int64) (*SubmissionView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	block := r.blocks[height]
	if block == nil {
		return nil, fmt.Errorf("L2 block %d not found", height)
	}
	if block.Status != BlockPending {
		return nil, fmt.Errorf("block already %s", block.Status)
	}
	if r.l1 == nil {
		return nil, fmt.Errorf("未配置 L1 提交器")
	}

	proof := L1Proof{
		L2Height: block.Height, BatchIndex: block.BatchIndex,
		TxCount: block.TxCount, StateRoot: block.StateRoot,
		PrevStateRoot: block.PrevStateRoot,
		Proposer:      block.Proposer, Timestamp: block.Timestamp,
		ChallengeDeadline: block.Timestamp + r.cfg.ChallengePeriod,
	}
	l1Hash, err := r.l1.SubmitStateRoot(proof)
	if err != nil {
		return nil, err
	}
	block.L1TxHash = l1Hash
	block.Status = BlockSubmitted
	if err := r.store.SaveBlock(*block); err != nil {
		return nil, err
	}
	return &SubmissionView{
		OK: true, L2Height: height, L1TxHash: l1Hash,
		StateRoot:         block.StateRoot,
		ChallengeDeadline: proof.ChallengeDeadline,
	}, nil
}

// FinalizeBlock 挑戰期過後最終確認 L2 塊。now<=0 取當前時間。
func (r *Rollup) FinalizeBlock(height int64, now int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	block := r.blocks[height]
	if block == nil {
		return fmt.Errorf("block %d not found", height)
	}
	if block.Status != BlockSubmitted {
		return fmt.Errorf("block status=%s, not submitted", block.Status)
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	deadline := block.Timestamp + r.cfg.ChallengePeriod
	if now < deadline {
		return fmt.Errorf("challenge period not over, remaining %d", deadline-now)
	}
	block.Status = BlockFinalized
	return r.store.SaveBlock(*block)
}

// ChallengeView 為欺詐挑戰結果。
type ChallengeView struct {
	OK               bool   `json:"ok"`
	ChallengeSuccess bool   `json:"challenge_success"`
	L2Height         int64  `json:"l2_height"`
	ClaimedRoot      string `json:"claimed_root,omitempty"`
	CorrectRoot      string `json:"correct_root"`
	Message          string `json:"message"`
}

// ChallengeBlock 在挑戰期內基於執行前快照真實重放，比對狀態根。
func (r *Rollup) ChallengeBlock(height, now int64) (*ChallengeView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	block := r.blocks[height]
	if block == nil {
		return nil, fmt.Errorf("block %d not found", height)
	}
	if block.Status != BlockSubmitted {
		return nil, fmt.Errorf("cannot challenge status=%s", block.Status)
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if now > block.Timestamp+r.cfg.ChallengePeriod {
		return nil, fmt.Errorf("challenge period over")
	}
	if block.PreSnapshot == nil {
		return nil, fmt.Errorf("missing pre_snapshot, cannot replay")
	}

	txs, err := r.store.GetTxsByHashes(block.TxHashes)
	if err != nil {
		return nil, err
	}
	replay := NewStateTree()
	replay.Restore(block.PreSnapshot)
	replayOK := true
	for i := range txs {
		if !applyOnTree(replay, &txs[i]) {
			replayOK = false
			break
		}
	}
	correctRoot, err := replay.ComputeRoot()
	if err != nil {
		return nil, err
	}
	fraud := !replayOK || correctRoot != block.StateRoot

	if !fraud {
		return &ChallengeView{
			OK: true, ChallengeSuccess: false, L2Height: height,
			CorrectRoot: correctRoot,
			Message:     "state root valid; challenge rejected",
		}, nil
	}

	// 欺詐成立：回滾當前狀態到執行前。
	r.tree.Restore(block.PreSnapshot)
	block.Status = BlockChallenged
	if err := r.store.SaveBlock(*block); err != nil {
		return nil, err
	}
	for addr := range block.PreSnapshot.Accounts {
		acc, _ := r.tree.GetAccount(addr)
		if err := r.store.SaveAccount(acc); err != nil {
			return nil, err
		}
	}
	// 後續塊全部失效。
	for h, b := range r.blocks {
		if h > height && (b.Status == BlockPending ||
			b.Status == BlockSubmitted || b.Status == BlockFinalized) {
			b.Status = BlockReverted
			if err := r.store.SaveBlock(*b); err != nil {
				return nil, err
			}
		}
	}
	return &ChallengeView{
		OK: true, ChallengeSuccess: true, L2Height: height,
		ClaimedRoot: block.StateRoot, CorrectRoot: correctRoot,
		Message: "fraud proven; block and later reverted",
	}, nil
}

// DepositView 為 L1→L2 存款結果。
type DepositView struct {
	OK         bool    `json:"ok"`
	Address    string  `json:"address"`
	Amount     float64 `json:"amount"`
	L1TxHash   string  `json:"l1_tx_hash"`
	NewBalance float64 `json:"new_balance"`
}

// DepositToL2 憑 L1 鎖定交易在 L2 鑄造等額資產（同一 L1 交易只入賬一次）。
func (r *Rollup) DepositToL2(address string, amount float64, l1TxHash string) (*DepositView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if amount <= 0 {
		return nil, fmt.Errorf("invalid amount")
	}
	if l1TxHash == "" {
		return nil, fmt.Errorf("l1_tx_hash required")
	}
	processed, err := r.store.IsDepositProcessed(l1TxHash)
	if err != nil {
		return nil, err
	}
	if processed {
		return nil, fmt.Errorf("deposit already credited (replay rejected)")
	}

	r.tree.SetAccount(L2Account{Address: address,
		Balance: r.tree.Balance(address) + amount,
		Nonce:   r.treeNonce(address), StorageRoot: ZeroRoot})
	acc, _ := r.tree.GetAccount(address)
	if err := r.store.SaveAccount(acc); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	depTx := L2Transaction{
		TxHash:   "deposit_" + l1TxHash,
		FromAddr: "L1_BRIDGE", ToAddr: address,
		Amount: amount, Status: StatusConfirmed,
		Ts: now, Signature: "deposit", Pubkey: "L1_BRIDGE",
	}
	if err := r.store.SaveTx(depTx); err != nil {
		return nil, err
	}
	if err := r.store.MarkDepositProcessed(l1TxHash, now); err != nil {
		return nil, err
	}
	return &DepositView{
		OK: true, Address: address, Amount: amount,
		L1TxHash: l1TxHash, NewBalance: acc.Balance,
	}, nil
}

func (r *Rollup) treeNonce(addr string) int64 {
	a, _ := r.tree.GetAccount(addr)
	return a.Nonce
}

// WithdrawView 為 L2→L1 提款結果。
type WithdrawView struct {
	OK                bool    `json:"ok"`
	WithdrawID        string  `json:"withdraw_id"`
	Address           string  `json:"address"`
	Amount            float64 `json:"amount"`
	NewBalance        float64 `json:"new_balance"`
	Status            string  `json:"status"`
	ChallengeDeadline int64   `json:"challenge_deadline"`
}

// WithdrawToL1 在 L2 燒毀資產並發起提款（挑戰期後可在 L1 領取）。
func (r *Rollup) WithdrawToL1(address string, amount float64) (*WithdrawView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc, ok := r.tree.GetAccount(address)
	if !ok || acc.Balance < amount || amount <= 0 {
		return nil, fmt.Errorf("insufficient L2 balance")
	}
	now := time.Now().Unix()
	acc.Balance -= amount
	r.tree.SetAccount(acc)
	if err := r.store.SaveAccount(acc); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("withdraw_%d_%s", now, short(address))
	wTx := L2Transaction{
		TxHash: id, FromAddr: address, ToAddr: "L1_BRIDGE",
		Amount: amount, Status: StatusPending, Ts: now,
		Signature: "withdraw", Pubkey: "L1_BRIDGE",
	}
	if err := r.store.SaveTx(wTx); err != nil {
		return nil, err
	}
	w := Withdrawal{
		ID: id, Address: address, Amount: amount,
		Status: StatusPending, CreatedAt: now,
	}
	if err := r.store.SaveWithdrawal(w); err != nil {
		return nil, err
	}
	return &WithdrawView{
		OK: true, WithdrawID: id, Address: address, Amount: amount,
		NewBalance: acc.Balance, Status: StatusPending,
		ChallengeDeadline: now + r.cfg.ChallengePeriod,
	}, nil
}

// CompleteWithdrawal 挑戰期過後把提款標為可在 L1 領取。
func (r *Rollup) CompleteWithdrawal(id string, now int64) (*Withdrawal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, err := r.store.GetWithdrawal(id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("withdrawal not found")
	}
	if w.Status == "completed" {
		return w, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	if now < w.CreatedAt+r.cfg.ChallengePeriod {
		return nil, fmt.Errorf("challenge period not over, remaining %d",
			w.CreatedAt+r.cfg.ChallengePeriod-now)
	}
	w.Status = "completed"
	w.FinalizedAt = now
	if err := r.store.SaveWithdrawal(*w); err != nil {
		return nil, err
	}
	return w, nil
}

func short(addr string) string {
	if len(addr) > 10 {
		return addr[len(addr)-8:]
	}
	return addr
}
