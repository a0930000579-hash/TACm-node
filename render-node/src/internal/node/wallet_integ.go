package node

import (
	"log"

	"tacm/internal/chaindb"
	"tacm/internal/wallet"
)

// Wallet 返回節點錢包服務（RPC/Web 層使用）。
func (n *Node) Wallet() *wallet.Service { return n.walletSvc }

// syncGenesisAlloc M118：把創世塊（height 0）的分配交易同步進 wallet 帳本。
// 修正：創世分配（genalloc/community_pool 等）僅寫入鏈上 DB，wallet 事務帳本視角缺餘額，
// 導致後續任何從創世地址轉帳的區塊在 syncWallet（ApplyBlock 嚴格連續）首筆扣款即報
// 「餘額不足」並永久跳號（多節點高負載壓力測試暴露的潛在 bug）。
// 節點初始化時掃描一次創世，Deposit 入帳；創世不屬 ApplyBlock 範圍（height>0），不影響 syncedHeight。
func (n *Node) syncGenesisAlloc() {
	if n.walletSvc == nil {
		return
	}
	g, err := n.db.GetBlock(0)
	if err != nil || g == nil {
		return
	}
	gtxs, err := n.db.GetTransactionsByBlock(0)
	if err != nil {
		return
	}
	for _, tx := range gtxs {
		if tx == nil || tx.ToAddr == "" {
			continue
		}
		amt, aerr := wallet.NewAmount(tx.Amount, wallet.AssetTACm)
		if aerr != nil || amt.IsZero() {
			continue
		}
		if derr := n.walletSvc.Deposit(tx.ToAddr, wallet.AssetTACm, amt, "genesis:alloc"); derr != nil {
			log.Printf("syncGenesisAlloc: %s: %v", tx.ToAddr, derr)
		}
	}
}

// syncWallet 把已落庫區塊的交易同步進錢包帳本。
// 冪等：wallet.ApplyBlock 僅接受連續高度；重組場景由節點層處理（M13 標註限制）。
func (n *Node) syncWallet(height int64, txs []chaindb.Transaction) error {
	if n.walletSvc == nil {
		return nil
	}
	return n.walletSvc.ApplyBlock(height, txs)
}
