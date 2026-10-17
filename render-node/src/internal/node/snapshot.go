package node

import (
	"fmt"
	"math/big"

	"tacm/internal/chaindb"
	"tacm/internal/wallet"
)

// SnapshotAccount 快照帳戶（含鏈上 nonce）。
type SnapshotAccount struct {
	Address      string `json:"address"`
	TACmBalance  string `json:"tacm_balance"`
	TiUSDBalance int64  `json:"tiusd_balance"`
	USDTBalance  int64  `json:"usdt_balance"`
	Nonce        int64  `json:"nonce"`
}

// SnapshotData 狀態快照（M149l）：創世＋tip 區塊＋全帳戶。
// 導入後新節點直接從 tip 接新塊，不需從創世重放整條鏈。
type SnapshotData struct {
	Version      int                   `json:"version"`
	Height       int64                 `json:"height"`
	GenesisHash  string                `json:"genesis_hash"`
	TipHash      string                `json:"tip_hash"`
	GenesisBlock *chaindb.Block        `json:"genesis_block,omitempty"`
	GenesisTxs   []chaindb.Transaction `json:"genesis_txs,omitempty"`
	TipBlock     *chaindb.Block        `json:"tip_block,omitempty"`
	TipTxs       []chaindb.Transaction `json:"tip_txs,omitempty"`
	Accounts     []SnapshotAccount     `json:"accounts"`
}

// ExportSnapshot 導出狀態快照（供 -snapshot-url 導入，秒級就緒）。
func (n *Node) ExportSnapshot() (*SnapshotData, error) {
	tip := n.db.GetTipHeight()
	g, err := n.db.GetBlock(0)
	if err != nil {
		return nil, fmt.Errorf("snapshot: 讀創世失敗: %w", err)
	}
	gt, err := n.db.GetBlockTxs(0)
	if err != nil {
		return nil, fmt.Errorf("snapshot: 讀創世交易失敗: %w", err)
	}
	tb, err := n.db.GetBlock(tip)
	if err != nil {
		return nil, fmt.Errorf("snapshot: 讀 tip 失敗: %w", err)
	}
	tt, err := n.db.GetBlockTxs(tip)
	if err != nil {
		return nil, fmt.Errorf("snapshot: 讀 tip 交易失敗: %w", err)
	}
	accs, err := n.Wallet().Store().Accounts()
	if err != nil {
		return nil, fmt.Errorf("snapshot: 讀帳戶失敗: %w", err)
	}
	out := &SnapshotData{
		Version: 1, Height: tip,
		GenesisBlock: g, GenesisTxs: gt,
		TipBlock: tb, TipTxs: tt,
		Accounts: make([]SnapshotAccount, 0, len(accs)),
	}
	if g != nil {
		out.GenesisHash = g.Hash
	}
	if tb != nil {
		out.TipHash = tb.Hash
	}
	for _, a := range accs {
		sa := SnapshotAccount{
			Address: a.Address, TiUSDBalance: a.TiUSDBalance, USDTBalance: a.USDTBalance,
		}
		if a.TACmBalance != nil {
			sa.TACmBalance = a.TACmBalance.String()
		} else {
			sa.TACmBalance = "0"
		}
		sa.Nonce = n.db.GetNonce(a.Address)
		out.Accounts = append(out.Accounts, sa)
	}
	return out, nil
}

// ImportSnapshot 導入狀態快照：重建創世＋tip 區塊、錢包帳戶與鏈上帳戶狀態。
func (n *Node) ImportSnapshot(sd *SnapshotData) error {
	if sd == nil || sd.GenesisBlock == nil || sd.TipBlock == nil {
		return fmt.Errorf("snapshot: 快照不完整（缺創世/tip）")
	}
	if sd.Height != sd.TipBlock.Height {
		return fmt.Errorf("snapshot: 高度不一致 %d vs %d", sd.Height, sd.TipBlock.Height)
	}
	if err := n.db.InsertBlock(sd.GenesisBlock, sd.GenesisTxs); err != nil {
		return fmt.Errorf("snapshot: 導入創世失敗: %w", err)
	}
	if err := n.db.InsertBlock(sd.TipBlock, sd.TipTxs); err != nil {
		return fmt.Errorf("snapshot: 導入 tip 失敗: %w", err)
	}
	accs := make([]wallet.Account, 0, len(sd.Accounts))
	for _, sa := range sd.Accounts {
		bi, ok := new(big.Int).SetString(sa.TACmBalance, 10)
		if !ok {
			bi = new(big.Int)
		}
		accs = append(accs, wallet.Account{
			Address: sa.Address, TACmBalance: bi,
			TiUSDBalance: sa.TiUSDBalance, USDTBalance: sa.USDTBalance,
		})
	}
	if err := n.Wallet().Store().ImportAccounts(accs); err != nil {
		return fmt.Errorf("snapshot: 導入錢包失敗: %w", err)
	}
	for _, sa := range sd.Accounts {
		if err := n.db.ImportAccountState(sa.Address, sa.TACmBalance, sa.Nonce); err != nil {
			return fmt.Errorf("snapshot: 導入鏈上帳戶失敗: %w", err)
		}
	}
	return nil
}
