package node

import (
	"log"
	"math/big"
	"strings"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/wallet"
)

// healWalletSync M103：錢包帳本與 chain_stats 自癒。
// 背景 goroutine，節點啟動後執行：
//  1. 若 wallet syncedHeight 落後於鏈高 → 逐塊補同步（單塊失敗跳過並 log，
//     避免 ApplyBlock 嚴格連續檢查造成永久卡死——即「總產出/錢包餘額停滯」的根因）。
//  2. 重放鏈上全部 coinbase 交易 → 以審計同源口徑修正 chain_stats.total_mined_tacm，
//     使「全鏈總產出」與審計重放一致（不再出現 10770 vs 16 萬 的矛盾）。
func (n *Node) healWalletSync() {
	if n.walletSvc == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("healWalletSync panic: %v", r)
		}
	}()
	tip := n.db.GetTipHeight()
	// M118：補創世分配（創世塊可能在節點初始化後才寫入——測試/私有鏈常見）。
	// 必須在 tip<=0 早退之前執行：純創世鏈 tip=0 也需同步創世分配。
	n.syncGenesisAlloc()
	if tip <= 0 {
		return
	}
	last, err := n.walletSvc.SyncedHeight()
	if err != nil || last < 0 {
		last = 0
	}
	// 1) 逐塊補同步（僅落後時執行）。
	if last < tip {
		log.Printf("healWalletSync: wallet %d -> chain %d, backfilling…", last, tip)
		start := time.Now()
		var skipped int
		for h := last + 1; h <= tip; h++ {
			bd, err := n.GetBlockDetail(h)
			if err != nil || bd == nil || bd.Block == nil {
				// 鏈上塊缺失：推進高度避免卡死（鏈本身不連續時由審計標記）。
				_ = n.walletSvc.ApplyBlockRelaxed(h, nil)
				skipped++
				continue
			}
			var txs []chaindb.Transaction
			for _, t := range bd.Transactions {
				if t != nil {
					txs = append(txs, *t)
				}
			}
			if err := n.walletSvc.ApplyBlockRelaxed(h, txs); err != nil {
				log.Printf("healWalletSync: h=%d skipped: %v", h, err)
				skipped++
			}
			if h%2000 == 0 {
				log.Printf("healWalletSync: h=%d (%d skipped)", h, skipped)
			}
		}
		log.Printf("healWalletSync: backfill done in %v, skipped=%d", time.Since(start), skipped)
	}
	// 2) 重放 coinbase 總額並修正 chain_stats（與審計同源：memo 前綴 coinbase）。
	total := big.NewInt(0)
	for h := int64(1); h <= tip; h++ {
		bd, err := n.GetBlockDetail(h)
		if err != nil || bd == nil || bd.Block == nil {
			continue
		}
		for _, t := range bd.Transactions {
			if t == nil || !isCoinbaseMemo(t.Memo) {
				continue
			}
			amt, aerr := wallet.NewAmount(strings.TrimSpace(t.Amount), wallet.AssetTACm)
			if aerr != nil {
				continue
			}
			if amt.Big != nil {
				total.Add(total, amt.Big)
			} else {
				total.Add(total, big.NewInt(amt.I64))
			}
		}
	}
	if err := n.walletSvc.SetMinedTacm(total); err != nil {
		log.Printf("healWalletSync: SetMinedTacm: %v", err)
	} else {
		log.Printf("healWalletSync: total_mined_tacm corrected to %s", total.String())
	}
}
