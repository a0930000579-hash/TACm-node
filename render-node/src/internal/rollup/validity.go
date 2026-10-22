package rollup

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	"tacm/internal/zk"
)

// 本檔為 v156 新增：把已執行的 L2 批次（L2Block）轉成「有效性證明」
// （validium 風格的簡化 ZK 證明），並提供無狀態驗證入口。
//
// 與既有樂觀機制的關係：
//   - 既有：CreateBatch 打包 → SubmitToL1 提交狀態根 → ChallengeBlock 基於
//     pre-snapshot 重放的欺詐證明（fraud proof）。本檔**不取代**它們，而是補上
//     「有效性」語義：每個批次可產生一份自包含證明（tx-root＋觸及葉 pre→post＋
//     約束編碼＋提議者批簽名），輕節點/L1 用 internal/zk 無狀態驗證，
//     不需持有完整狀態樹或等待挑戰窗口。
//   - 既有公開 API（New/StartService/SubmitTransaction/CreateBatch/SubmitToL1/
//     ChallengeBlock/FinalizeBlock）全部保持不變。

// canonAmt 把 float64 金額轉成 canonical 十進位字串（與簽名語境一致）。
func canonAmt(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ValidityProofFor 為指定高度的已執行批次重放並組裝有效性證明。
// 證明自包含：pre/post 觸及葉由執行前快照重放得出，輕節點可無狀態驗證。
func (r *Rollup) ValidityProofFor(height int64) (*zk.RollupValidityProof, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	block := r.blocks[height]
	if block == nil {
		return nil, fmt.Errorf("rollup: block %d not found", height)
	}
	if block.PreSnapshot == nil {
		return nil, fmt.Errorf("rollup: block %d 缺少 pre_snapshot，無法產生有效性證明", height)
	}

	txs, err := r.store.GetTxsByHashes(block.TxHashes)
	if err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, fmt.Errorf("rollup: block %d 無交易", height)
	}

	// 重放樹：從執行前快照出發，逐筆執行並記錄觸及賬戶的 pre/post。
	replay := NewStateTree()
	replay.Restore(block.PreSnapshot)

	touchedSet := map[string]bool{}
	zkTxs := make([]zk.RollupTx, 0, len(txs))
	// pre 值一律取自執行前快照（不是逐步演進的 replay 樹）。
	snap := block.PreSnapshot.Accounts
	preVal := map[string]L2Account{}
	preOf := func(addr string) L2Account {
		if a, ok := snap[addr]; ok {
			return a
		}
		return L2Account{Address: addr, StorageRoot: ZeroRoot}
	}
	for i := range txs {
		tx := &txs[i]
		preVal[tx.FromAddr] = preOf(tx.FromAddr)
		preVal[tx.ToAddr] = preOf(tx.ToAddr)
		touchedSet[tx.FromAddr] = true
		touchedSet[tx.ToAddr] = true

		if !applyOnTree(replay, tx) {
			return nil, fmt.Errorf("rollup: block %d 交易 #%d (%s) 重放失敗（約束違規）",
				height, i, tx.TxHash)
		}
		zkTxs = append(zkTxs, zk.RollupTx{
			TxHash: tx.TxHash, From: tx.FromAddr, To: tx.ToAddr,
			Amount: canonAmt(tx.Amount), Fee: canonAmt(tx.Fee),
			Nonce: tx.Nonce, Ts: tx.Ts,
			Signature: tx.Signature, Pubkey: tx.Pubkey,
		})
	}

	addrs := make([]string, 0, len(touchedSet))
	for a := range touchedSet {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	touched := make([]zk.RollupAccTransition, 0, len(addrs))
	for _, a := range addrs {
		pre := preVal[a]
		post, _ := replay.GetAccount(a)
		touched = append(touched, zk.RollupAccTransition{
			Address:     a,
			PreBalance:  canonAmt(pre.Balance),
			PreNonce:    pre.Nonce,
			PostBalance: canonAmt(post.Balance),
			PostNonce:   post.Nonce,
		})
	}

	// 提議者批簽名（若配置了 ProposerWIF）。
	var priv []byte
	var signerPub string
	if r.cfg.ProposerWIF != "" {
		if kp, err := parseSignerKey(r.cfg.ProposerWIF); err == nil {
			priv = kp.PrivateKey()
			signerPub = hex.EncodeToString(kp.PublicKeyCompressed())
		}
	}
	return zk.BuildRollupValidityProof(
		block.BatchIndex, block.PrevStateRoot, block.Timestamp,
		zkTxs, touched, priv, signerPub)
}

// VerifyBatchValidity 重產生證明並無狀態驗證（餘額非負、nonce、守恆、
// 簽名、post root 承諾全部通過）。返回 error＝批次無效。
func (r *Rollup) VerifyBatchValidity(height int64) error {
	p, err := r.ValidityProofFor(height)
	if err != nil {
		return err
	}
	return zk.VerifyRollupValidityProof(p)
}

// BatchView 為單一批次對外視圖（含有效性證明）。
type BatchView struct {
	*L2Block
	Validity *zk.RollupValidityProof `json:"validity_proof,omitempty"`
}

// Batch 回傳指定批次（不含證明）；不存在返回 nil。
func (r *Rollup) Batch(height int64) *L2Block {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blocks[height]
}

// BatchWithProof 回傳指定批次及其有效性證明。
func (r *Rollup) BatchWithProof(height int64) (*BatchView, error) {
	b := r.Batch(height)
	if b == nil {
		return nil, nil
	}
	p, err := r.ValidityProofFor(height)
	if err != nil {
		// 證明生成失敗不影響塊本身的讀取。
		return &BatchView{L2Block: b}, nil
	}
	return &BatchView{L2Block: b, Validity: p}, nil
}

// BatchesList 按高度升序回傳全部批次（供列表/頁面）。
func (r *Rollup) BatchesList(limit int) []*L2Block {
	r.mu.Lock()
	defer r.mu.Unlock()
	heights := make([]int64, 0, len(r.blocks))
	for h := range r.blocks {
		heights = append(heights, h)
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	if limit > 0 && len(heights) > limit {
		heights = heights[len(heights)-limit:]
	}
	out := make([]*L2Block, 0, len(heights))
	for _, h := range heights {
		out = append(out, r.blocks[h])
	}
	return out
}

// StateRootView 為當前狀態根視圖。
type StateRootView struct {
	BlockHeight  int64  `json:"block_height"`
	StateRoot    string `json:"state_root"`
	AccountCount int    `json:"account_count"`
	Valid        bool   `json:"valid"` // 最近一批有效性證明是否通過
}

// CurrentStateRoot 回傳當前狀態樹根與高度。
func (r *Rollup) CurrentStateRoot() (*StateRootView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, err := r.tree.ComputeRoot()
	if err != nil {
		return nil, err
	}
	view := &StateRootView{
		BlockHeight:  int64(len(r.blocks)),
		StateRoot:    root,
		AccountCount: r.tree.AccountCount(),
		Valid:        true,
	}
	// 若已有批次，順手自驗最近一批有效性（不為空才查）。
	if view.BlockHeight > 0 {
		// 只做輕量檢查：最近一批若曾被挑戰/回滾則標 invalid。
		if b := r.blocks[view.BlockHeight]; b != nil {
			view.Valid = b.Status != BlockChallenged && b.Status != BlockReverted
		}
	}
	return view, nil
}
