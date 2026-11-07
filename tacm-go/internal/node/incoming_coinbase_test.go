package node

// 追塊（OnIncomingBlock→verifyIncomingTxs）的 coinbase 口徑必須與重組/出塊路徑同源
// （ComputeCappedReward），否則快照 follower 會把衰減/截斷後的實際獎勵誤判為應為基礎獎勵
// （歷史缺陷：coinbase 總額=6.018 應為 10）。

import (
	"testing"

	"tacm/internal/chaindb"
)

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
