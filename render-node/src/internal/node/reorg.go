package node

import (
	"errors"
	"fmt"
	"strconv"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// ReorgChain 在共同分叉點之後切換到新鏈：驗證結構與累積難度，截斷舊鏈、
// 重放新塊並全量重建賬戶，確保最終狀態與新鏈一致。
func (h *p2pHost) ReorgChain(forkPoint int64, blocks []chaindb.Block, txs [][]chaindb.Transaction) error {
	n := h.n
	if len(blocks) == 0 {
		return errors.New("重組鏈為空")
	}
	if len(blocks) != len(txs) {
		return errors.New("blocks 與 txs 數量不一致")
	}
	if blocks[0].Height != forkPoint+1 {
		return errors.New("重組起始高度與分叉點不符")
	}

	// 逐塊結構驗證（連續性、prev_hash、coinbase、簽名、merkle、PoW）。
	var newCD int64
	for i := range blocks {
		b := &blocks[i]
		if b.Height != forkPoint+int64(i)+1 {
			return errors.New("重組塊高度不連續")
		}
		wantPrev := ""
		if i == 0 {
			fb, err := n.db.GetBlock(forkPoint)
			if err != nil {
				return err
			}
			if fb != nil {
				wantPrev = fb.Hash
			}
		} else {
			wantPrev = blocks[i-1].Hash
		}
		prev := ""
		if b.PrevHash != nil {
			prev = *b.PrevHash
		}
		if prev != wantPrev {
			return fmt.Errorf("重組塊 %d prev_hash 不匹配", b.Height)
		}
		if err := verifyBlockStructure(b, txs[i]); err != nil {
			return err
		}
		newCD += int64(b.Difficulty)
	}

	// 累積難度比較：僅當新鏈更重才切換。
	oldCD, err := n.db.CumulativeDifficulty(forkPoint, n.db.GetTipHeight())
	if err != nil {
		return err
	}
	if newCD <= oldCD {
		return errors.New("重組鏈累積難度不更重，拒絕切換")
	}

	// 截斷舊鏈 forkPoint 之後。
	if err := n.db.TruncateFromHeight(forkPoint + 1); err != nil {
		return err
	}
	// 重放新塊。
	for i := range blocks {
		b := blocks[i]
		b.TxCount = len(txs[i])
		if err := n.db.InsertBlock(&b, txs[i]); err != nil {
			_, _ = n.db.RebuildAccounts()
			return err
		}
		n.onNewBlock(b.Height, b.Hash)
	}
	// 全量重建賬戶，確保與新鏈絕對一致。
	if _, err := n.db.RebuildAccounts(); err != nil {
		return err
	}
	// 重組後合約集合可能不同，整體重建合約 code/storage。
	if err := n.RebuildContracts(); err != nil {
		return err
	}
	return nil
}

// verifyBlockStructure 驗證區塊的結構有效性（不依賴賬戶狀態）：
// coinbase 總額、普通交易簽名、Merkle 根、PoW。
func verifyBlockStructure(b *chaindb.Block, txs []chaindb.Transaction) error {
	if len(txs) == 0 {
		return errors.New("區塊缺少 coinbase 交易")
	}
	// coinbase：開頭連續 from 為空的交易（M58 鏈上瓜分可多筆），總額須等於區塊獎勵。
	cbTotal := 0.0
	i := 0
	for i < len(txs) && txs[i].FromAddr == "" {
		cb := txs[i]
		if cb.ToAddr == "" {
			return errors.New("coinbase 收款人缺失")
		}
		got, err := parseFloat64(cb.Amount)
		if err != nil {
			return fmt.Errorf("coinbase 金額非法: %s", cb.Amount)
		}
		cbTotal += got
		i++
	}
	wantReward := chaindb.BlockReward(b.Height)
	if abs(cbTotal-wantReward) > 1e-9 {
		return fmt.Errorf("coinbase 總額=%g 應為 %g", cbTotal, wantReward)
	}

	for j := i; j < len(txs); j++ {
		t := txs[j]
		m := map[string]any{
			"from": t.FromAddr, "to": t.ToAddr, "amount": t.Amount, "fee": t.Fee,
			"memo": t.Memo, "ts": t.Ts, "nonce": t.Nonce, "pubkey": t.Pubkey,
		}
		if !verifyTxSignature(m, t.Signature, t.FromAddr) {
			return fmt.Errorf("交易 %d（%s）簽名無效", j, t.TxHash)
		}
	}

	txHashes := make([]string, 0, len(txs))
	for i := range txs {
		txHashes = append(txHashes, txs[i].TxHash)
	}
	mroot, err := crypto.MerkleRootStrings(txHashes)
	if err != nil {
		return err
	}
	if mroot != b.MerkleRoot {
		return errors.New("Merkle 根不匹配")
	}

	prev := ""
	if b.PrevHash != nil {
		prev = *b.PrevHash
	}
	header := map[string]any{
		"height": b.Height, "prev_hash": prev, "merkle_root": b.MerkleRoot,
		"proposer": b.Proposer, "ts": b.Ts, "tx_count": len(txs),
		"nonce": b.Nonce, "difficulty": b.Difficulty,
	}
	ok, err := crypto.VerifyPow(header, b.Difficulty)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("PoW 驗證失敗")
	}
	return nil
}

func parseFloat64(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
