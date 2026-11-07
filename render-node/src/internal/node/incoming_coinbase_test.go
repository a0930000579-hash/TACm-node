package node

// 追塊（OnIncomingBlock→verifyIncomingTxs）的 coinbase 口徑必須與重組/出塊路徑同源
// （ComputeCappedReward），否則快照 follower 會把衰減/截斷後的實際獎勵誤判為應為基礎獎勵
// （歷史缺陷：coinbase 總額=6.018 應為 10）。

import (
	"sync"
	"testing"

	"tacm/internal/chaindb"
)

// TestProduceBlockNoDoubleCoinbaseRace：並發出塊競賽下，任何高度都只能有一套 coinbase
// （歷史缺陷：本地 produceBlock 與外來塊同高度各自 InsertBlock，INSERT OR REPLACE 留下
// 兩套 coinbase，coinbase 總額變成 2× 出塊獎勵）。chainWriteMu＋鎖內重讀 tip 必須擋下。
func TestProduceBlockNoDoubleCoinbaseRace(t *testing.T) {
	n := benchNodeT(t, 1)

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 3; attempt++ {
				err := n.produceBlock()
				// 競賽落敗（errBlockExists）或非提議者皆屬良性；其餘錯誤才需要回報。
				if err != nil && err != errBlockExists && err != errNotProposer {
					t.Errorf("produceBlock: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	tip := n.db.GetTipHeight()
	for h := int64(1); h <= tip; h++ {
		detail, err := n.GetBlockDetail(h)
		if err != nil || detail == nil {
			t.Fatalf("h=%d detail: %v", h, err)
		}
		mw, merr := n.walletSvc.ChainMinedTacm()
		if merr != nil {
			t.Fatalf("ChainMinedTacm h=%d: %v", h, merr)
		}
		want := chaindb.ComputeCappedReward(h, int64(n.cfg.BlockTime), mw)
		cbTotal := 0.0
		for _, tx := range detail.Transactions {
			if tx.FromAddr != "" {
				break
			}
			v, perr := parseFloat64(tx.Amount)
			if perr != nil {
				t.Fatalf("h=%d coinbase 非法: %s", h, tx.Amount)
			}
			cbTotal += v
		}
		// 硬性不變式：每塊 coinbase 嚴格等於應發獎勵，絕不允許 2×（雙倍增發）。
		if abs(cbTotal-want) > 1e-9 {
			t.Fatalf("h=%d coinbase=%g 應為 %g（疑似雙發）", h, cbTotal, want)
		}
	}

	// 同高度再餵一個競爭塊：P2PHost 必須良性忽略且不增發任何 coinbase。
	host := n.P2PHost()
	b, err := n.db.GetBlock(tip)
	if err != nil || b == nil {
		t.Fatalf("GetBlock tip: %v", err)
	}
	competing := *b
	competing.Hash = "f" + competing.Hash[1:] // 不同哈希、同高度
	if err := host.OnIncomingBlock(&competing, nil); err != nil {
		t.Fatalf("同高度競爭塊應被忽略，實際: %v", err)
	}
	if n.db.GetTipHeight() != tip {
		t.Fatalf("tip 不應改變: %d→%d", tip, n.db.GetTipHeight())
	}
}

// TestVerifyIncomingCoinbaseMatchesCappedReward：逐塊確認追塊驗證接受實際 coinbase，
// 且其期望與 ComputeCappedReward 完全一致。
func TestVerifyIncomingCoinbaseMatchesCappedReward(t *testing.T) {
	src := benchNodeT(t, 1)
	const blocks = 6
	for i := 0; i < blocks; i++ {
		if err := src.produceBlock(); err != nil {
			t.Fatalf("出塊 %d: %v", i+1, err)
		}
	}

	for h := int64(1); h <= blocks; h++ {
		b, err := src.db.GetBlock(h)
		if err != nil || b == nil {
			t.Fatalf("GetBlock %d: %v", h, err)
		}
		detail, err := src.GetBlockDetail(h)
		if err != nil || detail == nil {
			t.Fatalf("GetBlockDetail %d: %v", h, err)
		}
		txs := make([]chaindb.Transaction, 0, len(detail.Transactions))
		for _, tx := range detail.Transactions {
			txs = append(txs, *tx)
		}

		// 追塊路徑（錢包已發狀態已對齊）不應拒絕實際 coinbase。
		if err := verifyIncomingTxs(src, b, txs); err != nil {
			t.Fatalf("h=%d 追塊 coinbase 驗證失敗: %v", h, err)
		}

		// 顯式斷言：本塊 coinbase 總額 == ComputeCappedReward（同源權威口徑）。
		mw, merr := src.walletSvc.ChainMinedTacm()
		if merr != nil {
			t.Fatalf("ChainMinedTacm h=%d: %v", h, merr)
		}
		want := chaindb.ComputeCappedReward(h, int64(src.cfg.BlockTime), mw)
		cbTotal := 0.0
		for _, tx := range txs {
			if tx.FromAddr != "" {
				break
			}
			v, perr := parseFloat64(tx.Amount)
			if perr != nil {
				t.Fatalf("h=%d coinbase 金額非法: %s", h, tx.Amount)
			}
			cbTotal += v
		}
		if abs(cbTotal-want) > 1e-9 {
			t.Fatalf("h=%d coinbase=%g 應為 capped=%g", h, cbTotal, want)
		}
	}
}
