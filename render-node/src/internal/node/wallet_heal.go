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
	// 單條查詢串流所有 coinbase 原始金額，走 big.Int 精確口徑，不逐塊 GetBlockDetail。
	total := big.NewInt(0)
	herr := n.db.ForEachCoinbaseAmount(func(amount string) error {
		amt, aerr := wallet.NewAmount(strings.TrimSpace(amount), wallet.AssetTACm)
		if aerr != nil {
			// 與舊邏輯一致：無法解析的金額跳過，不中斷整體重放。
			return nil
		}
		if amt.Big != nil {
			total.Add(total, amt.Big)
		} else {
			total.Add(total, big.NewInt(amt.I64))
		}
		return nil
	})
	if herr != nil {
		log.Printf("healWalletSync: coinbase 重放失敗: %v", herr)
		return
	}
	if err := n.walletSvc.SetMinedTacm(total); err != nil {
		log.Printf("healWalletSync: SetMinedTacm: %v", err)
	} else {
		log.Printf("healWalletSync: total_mined_tacm corrected to %s", total.String())
	}
}

// walletReconcileInterval 為錢包週期對齊安全網的間隔。
const walletReconcileInterval = 30 * time.Second

// StartWalletReconciler 啟動錢包週期對齊背景循環：每 30 秒檢查錢包已同步游標是否
// 落後鏈頂，落後即觸發一次 reconcileWallet。重組後即使漏接事件，最遲 30 秒內自動
// 對齊，不需重啟。循環隨 stopCh 結束，計入 n.wg。
func (n *Node) StartWalletReconciler() {
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		stop := n.stopChannel()
		t := time.NewTicker(walletReconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				n.reconcileWallet()
			}
		}
	}()
}

// reconcileWallet 把錢包帳本從當前已同步游標補齊到鏈頂。採單一飛行（singleflight）
// 防護：重組風暴時多個觸發僅一個 backfill 實際執行，其餘直接返回，不堆疊 goroutine。
// 補同步沿用 ApplyBlockRelaxed（單塊失敗仍推進游標並記 log，避免嚴格連續檢查永久卡死）。
// 冪等：錢包已追平時僅讀兩個高度後立即返回，開銷極低。不修改鏈上資料，只對齊派生視圖。
func (n *Node) reconcileWallet() {
	if n.walletSvc == nil {
		return
	}
	n.walletReconcileMu.Lock()
	if n.walletReconciling {
		n.walletReconcileMu.Unlock()
		return
	}
	n.walletReconciling = true
	n.walletReconcileMu.Unlock()
	defer func() {
		n.walletReconcileMu.Lock()
		n.walletReconciling = false
		n.walletReconcileMu.Unlock()
		if r := recover(); r != nil {
			log.Printf("reconcileWallet panic: %v", r)
		}
	}()

	// 補創世分配（創世塊可能在初始化後才寫入）。
	n.syncGenesisAlloc()
	tip := n.db.GetTipHeight()
	if tip <= 0 {
		return
	}
	last, err := n.walletSvc.SyncedHeight()
	if err != nil || last < 0 {
		last = 0
	}
	if last >= tip {
		return
	}
	log.Printf("reconcileWallet: wallet %d -> chain %d, backfilling…", last, tip)
	start := time.Now()
	var skipped int
	for h := last + 1; h <= tip; h++ {
		bd, berr := n.GetBlockDetail(h)
		if berr != nil || bd == nil || bd.Block == nil {
			// 鏈上塊缺失：推進游標避免卡死（鏈不連續時由審計標記）。
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
			log.Printf("reconcileWallet: h=%d skipped: %v", h, err)
			skipped++
		}
	}
	log.Printf("reconcileWallet: done in %v, skipped=%d", time.Since(start), skipped)
}
