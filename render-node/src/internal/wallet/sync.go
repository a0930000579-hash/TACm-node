package wallet

import (
	"database/sql"
	"fmt"
	"math/big"
	"time"

	"tacm/internal/chaindb"
)

// RewardPoolAddr 鏈上獎勵池帳戶（coinbase 按 PoolShareBps 挹注；交易所手續費可結算入池）。
const RewardPoolAddr = "reward_pool"

// PoolShareBps 每區塊 coinbase 挹注獎勵池的比例（16% = 1600 bp，對齊 M70 節點9%/池16%/礦工75% 經濟模型）。
const PoolShareBps = 1600

// ApplyBlock 把鏈上一個已確認區塊的交易同步到錢包帳本（TACm 資產）。
// 冪等：僅接受嚴格連續的高度（lastApplied+1）；重複/跳號（含鏈重組）返回錯誤。
// 分錄：coinbase → 提議者獎勵入帳；普通交易 → 付款人扣款（含手續費）、
// 收款人入帳、手續費入「fee」帳戶。整個區塊單一事務。
func (s *Service) ApplyBlock(height int64, txs []chaindb.Transaction) error {
	if height <= 0 {
		return fmt.Errorf("wallet: 區塊高度必須大於 0")
	}
	last, err := s.st.syncedHeight()
	if err != nil {
		return err
	}
	if height <= last {
		return nil // 已同步，冪等
	}
	if height != last+1 {
		return fmt.Errorf("wallet: 同步高度跳號 last=%d new=%d（鏈重組需重新同步）", last, height)
	}

	entries := make([]LedgerEntry, 0, len(txs)*3)
	var minedTotal *big.Int // M34：全鏈出塊產出累計（僅 coinbase :miner/:proposer/:pool）
	for i, tx := range txs {
		amount, err := parseTxAmount(tx.Amount, AssetTACm)
		if err != nil {
			return fmt.Errorf("wallet: 交易 %s 金額解析失敗: %w", tx.TxHash, err)
		}
		fee, err := parseTxAmount(tx.Fee, AssetTACm)
		if err != nil {
			return fmt.Errorf("wallet: 交易 %s 手續費解析失敗: %w", tx.TxHash, err)
		}
		memo := fmt.Sprintf("block:%d:%d:%s", height, i, tx.TxHash)
		if tx.FromAddr == "" {
			// M58：coinbase 已在鏈上由出塊者按礦工表瓜分（多筆：miner/pool/reserve），
			// 錢包層照單入帳，不再本地再分配 → 所有節點帳本一致。
			entries = append(entries,
				Entry(KindReward, tx.ToAddr, AssetTACm, amount, memo),
			)
			if minedTotal == nil {
				minedTotal = new(big.Int)
			}
			minedTotal.Add(minedTotal, amount)
			continue
		}
		if tx.FromAddr != tx.ToAddr {
			total := new(big.Int).Add(amount, fee)
			entries = append(entries,
				Entry(KindTransferOut, tx.FromAddr, AssetTACm, new(big.Int).Neg(total), memo),
				Entry(KindTransferIn, tx.ToAddr, AssetTACm, amount, memo),
				Entry(KindFee, "fee", AssetTACm, fee, memo),
			)
		} else {
			// 自轉：僅扣手續費。
			entries = append(entries, Entry(KindFee, tx.FromAddr, AssetTACm, new(big.Int).Neg(fee), memo))
		}
	}
	if err := s.st.ApplyEntries(entries); err != nil {
		return err
	}
	// M34：全鏈出塊產出計數器（O(1)，正確反映全鏈總產出）。
	if minedTotal != nil {
		if err := s.st.AddMinedTacm(minedTotal); err != nil {
			return err
		}
	}
	return s.st.markSynced(height)
}

// parseTxAmount 解析鏈上十進制金額字符串為 *big.Int（任意精度）。
func parseTxAmount(s string, asset Asset) (*big.Int, error) {
	amt, err := NewAmount(s, asset)
	if err != nil {
		return nil, err
	}
	if amt.Big == nil {
		return big.NewInt(amt.I64), nil
	}
	return amt.Big, nil
}

// syncedHeight 返回錢包已同步的鏈上高度。
func (s *Store) syncedHeight() (int64, error) {
	var h int64
	err := s.db.QueryRow(`SELECT height FROM wallet_sync WHERE id = 1`).Scan(&h)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("wallet: 讀取同步高度: %v", err)
	}
	return h, nil
}

// markSynced 記錄已同步高度。
func (s *Store) markSynced(height int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO wallet_sync (id, height, ts) VALUES (1, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET height = excluded.height, ts = excluded.ts`,
		height, time.Now().Unix()); err != nil {
		return fmt.Errorf("wallet: 記錄同步高度: %v", err)
	}
	return nil
}

// SyncedHeight 返回錢包已同步高度（供診斷）。
func (s *Service) SyncedHeight() (int64, error) { return s.st.syncedHeight() }

// ApplyBlockRelaxed M103：補同步專用——正常 ApplyBlock；單塊失敗時仍推進
// 同步高度（記 log 由呼叫方負責），避免 wallet 帳本因單塊異常而永久卡死。
// 注意：跳過的塊不會進入本地 ledger，鏈上資料不受影響；全鏈審計會顯示差異。
func (s *Service) ApplyBlockRelaxed(height int64, txs []chaindb.Transaction) error {
	if err := s.ApplyBlock(height, txs); err != nil {
		_ = s.st.markSynced(height)
		return err
	}
	return nil
}

// SetMinedTacm M103：直接設全鏈出塊產出計數器（自癒修正用）。
func (s *Service) SetMinedTacm(total *big.Int) error {
	if s.st == nil {
		return fmt.Errorf("wallet: store 未初始化")
	}
	return s.st.SetMinedTacm(total)
}
