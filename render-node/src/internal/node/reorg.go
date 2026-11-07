package node

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
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

	// 逐塊結構驗證（連續性、prev_hash、coinbase、簽名、merkle、PoT）。
	for i := range blocks {
		b := &blocks[i]
		if b.Height != forkPoint+int64(i)+1 {
			return errors.New("重組塊高度不連續")
		}
		wantPrev := ""
		if i == 0 && forkPoint >= 0 {
			fb, err := n.db.GetBlock(forkPoint)
			if err != nil {
				return err
			}
			if fb != nil {
				wantPrev = fb.Hash
			}
		} else if i > 0 {
			wantPrev = blocks[i-1].Hash
		}
		// i==0 && forkPoint<0：整鏈替換（信任來源鏈創世），創世 prev_hash 不校驗。
		prev := ""
		if b.PrevHash != nil {
			prev = *b.PrevHash
		}
		if prev != wantPrev {
			return fmt.Errorf("重組塊 %d prev_hash 不匹配", b.Height)
		}
		cp := ""
		if addr, cerr := h.n.CommunityPoolAddress(); cerr == nil {
			cp = addr
		}
		// 硬煞車驗證：取本塊前全鏈已發（walletSvc 同步帳本）傳入 verifyBlockStructure，
		// 與出塊側同源（ComputeCappedReward），確保 coinbase 總額全網一致。
		var minedWei *big.Int
		if h.n.walletSvc != nil {
			if mw, merr := h.n.walletSvc.ChainMinedTacm(); merr == nil {
				minedWei = mw
			}
		}
		if err := verifyBlockStructure(b, txs[i], int64(h.n.cfg.BlockTime), cp, minedWei); err != nil {
			return err
		}
	}

	// PoT（時間權證）：由分叉後區塊數（鏈長）決定，更長才切換。
	oldLen := n.db.GetTipHeight() - forkPoint
	if int64(len(blocks)) <= oldLen {
		return errors.New("重組鏈未更長（PoT 時間槽數不佔優），拒絕切換")
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

// ReorgChainFull 以「整條新鏈」做一次性的總權重決策後切換：驗證全部區塊、計算新鏈總
// 累積難度，與舊鏈 forkPoint 之後的總累積難度比較（更重才切換），再截斷一次並逐塊重放。
// 解決分段 ReorgChain 只拿單段 CD 對舊鏈全 CD 比較、長鏈領先時首段永遠被拒的問題。
// forkPoint<0 為整鏈替換（信任來源鏈創世），不做舊 CD 比較。
func (h *p2pHost) ReorgChainFull(forkPoint int64, blocks []chaindb.Block, txs [][]chaindb.Transaction) error {
	n := h.n
	if len(blocks) == 0 || len(blocks) != len(txs) {
		return errors.New("重組區塊/交易為空或數量不符")
	}
	if forkPoint >= 0 && blocks[0].Height != forkPoint+1 {
		return errors.New("重組起始高度與分叉點不符")
	}

	// 全鏈結構驗證（PoT 權重＝區塊數，於下方比較）。
	for i := range blocks {
		b := &blocks[i]
		if forkPoint >= 0 && b.Height != forkPoint+int64(i)+1 {
			return errors.New("重組塊高度不連續")
		}
		wantPrev := ""
		if i == 0 && forkPoint >= 0 {
			fb, err := n.db.GetBlock(forkPoint)
			if err != nil {
				return err
			}
			if fb != nil {
				wantPrev = fb.Hash
			}
		} else if i > 0 {
			wantPrev = blocks[i-1].Hash
		}
		prev := ""
		if b.PrevHash != nil {
			prev = *b.PrevHash
		}
		if forkPoint >= 0 && prev != wantPrev {
			return fmt.Errorf("重組塊 %d prev_hash 不匹配", b.Height)
		}
		cp := ""
		if addr, cerr := n.CommunityPoolAddress(); cerr == nil {
			cp = addr
		}
		var minedWei *big.Int
		if n.walletSvc != nil {
			if mw, merr := n.walletSvc.ChainMinedTacm(); merr == nil {
				minedWei = mw
			}
		}
		if err := verifyBlockStructure(b, txs[i], int64(n.cfg.BlockTime), cp, minedWei); err != nil {
			return err
		}
	}

	if forkPoint >= 0 {
		// PoT（時間權證，非 PoW）：每個合法區塊代表一個時間槽、權重相等，
		// 故由「分叉後的區塊數（鏈長）」決定 canonical chain——比特幣最長鏈
		// 規則的 PoT 版本，不使用會隨節點分歧的累積難度。
		oldLen := n.db.GetTipHeight() - forkPoint
		newLen := int64(len(blocks))
		if newLen <= oldLen {
			return errors.New("重組鏈未更長（PoT 時間槽數不佔優），拒絕切換")
		}
	}

	// 截斷舊鏈一次（整鏈替換時從創世之後清）。
	trunc := forkPoint + 1
	if forkPoint < 0 {
		trunc = 0
	}
	if err := n.db.TruncateFromHeight(trunc); err != nil {
		return err
	}
	for i := range blocks {
		b := blocks[i]
		b.TxCount = len(txs[i])
		if err := n.db.InsertBlock(&b, txs[i]); err != nil {
			_, _ = n.db.RebuildAccounts()
			return err
		}
		n.onNewBlock(b.Height, b.Hash)
	}
	if _, err := n.db.RebuildAccounts(); err != nil {
		return err
	}
	if err := n.RebuildContracts(); err != nil {
		return err
	}
	return nil
}

// ReorgChainTieBreak 用於同高度（等長）分叉的確定性收斂：僅當新鏈 tip 哈希
// 字典序更小時才切換。全網各節點獨立計算皆得到同一 winner，無節點特權。
func (h *p2pHost) ReorgChainTieBreak(forkPoint int64, blocks []chaindb.Block, txs [][]chaindb.Transaction) error {
	n := h.n
	if forkPoint < 0 {
		return errors.New("tie-break 不適用於整鏈替換")
	}
	if len(blocks) == 0 || len(blocks) != len(txs) {
		return errors.New("重組區塊/交易為空或數量不符")
	}
	if blocks[0].Height != forkPoint+1 {
		return errors.New("重組起始高度與分叉點不符")
	}
	// 必須等長，且新鏈 tip 哈希字典序更小（確定性）。
	if n.db.GetTipHeight()-forkPoint != int64(len(blocks)) {
		return errors.New("tie-break 僅適用等長分叉，長度不同應走 ReorgChainFull")
	}
	oldTip, err := n.db.GetBlock(n.db.GetTipHeight())
	if err != nil || oldTip == nil {
		return errors.New("無法讀取本地鏈頂")
	}
	newHash := blocks[len(blocks)-1].Hash
	if newHash >= oldTip.Hash {
		return errors.New("本地鏈 tip 哈希不更大，保留本地（tie-break）")
	}

	// 逐塊結構驗證（連續性、prev_hash、coinbase、簽名、merkle、PoT 封印）。
	for i := range blocks {
		b := &blocks[i]
		if b.Height != forkPoint+int64(i)+1 {
			return errors.New("重組塊高度不連續")
		}
		wantPrev := ""
		if i == 0 {
			fb, ferr := n.db.GetBlock(forkPoint)
			if ferr != nil {
				return ferr
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
		cp := ""
		if addr, cerr := n.CommunityPoolAddress(); cerr == nil {
			cp = addr
		}
		var minedWei *big.Int
		if n.walletSvc != nil {
			if mw, merr := n.walletSvc.ChainMinedTacm(); merr == nil {
				minedWei = mw
			}
		}
		if err := verifyBlockStructure(b, txs[i], int64(n.cfg.BlockTime), cp, minedWei); err != nil {
			return err
		}
	}

	// 截斷、重放、重建賬戶與合約。
	if err := n.db.TruncateFromHeight(forkPoint + 1); err != nil {
		return err
	}
	for i := range blocks {
		b := blocks[i]
		b.TxCount = len(txs[i])
		if err := n.db.InsertBlock(&b, txs[i]); err != nil {
			_, _ = n.db.RebuildAccounts()
			return err
		}
		n.onNewBlock(b.Height, b.Hash)
	}
	if _, err := n.db.RebuildAccounts(); err != nil {
		return err
	}
	if err := n.RebuildContracts(); err != nil {
		return err
	}
	return nil
}

// verifyBlockStructure 驗證區塊的結構有效性（不依賴賬戶狀態）：
// coinbase 總額、普通交易簽名、Merkle 根、PoT。
// minedWei 為本塊前全鏈已發總量（wei），用於硬煞車（剩餘可挖截斷）驗證，
// 與出塊側 node.go 共用 chaindb.ComputeCappedReward，確保全網一致。
func verifyBlockStructure(b *chaindb.Block, txs []chaindb.Transaction, blockTimeSec int64, communityPoolAddr string, minedWei *big.Int) error {
	if b.Height == 0 {
		// 創世為特殊塊（社群基金 + 空 Merkle 根），結構由來源鏈信任承接。
		return nil
	}
	// M150h-硬煞車：預期獎勵 = ComputeCappedReward（模型獎勵 min 剩餘可挖）。
	// 已達上限（剩餘≤0）→ 本塊零增發，容許無 coinbase（鏈續跑處理交易）。
	wantReward := chaindb.ComputeCappedReward(b.Height, blockTimeSec, minedWei)
	if wantReward <= 1e-9 {
		return nil
	}
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
	if abs(cbTotal-wantReward) > 1e-9 {
		return fmt.Errorf("coinbase 總額=%g 應為 %g（emission=capped）", cbTotal, wantReward)
	}

	for j := i; j < len(txs); j++ {
		t := txs[j]
		// M151c：治理 treasury 撥款交易（memo=gov:treasury:N、無簽名）——
		// 由出塊引擎依治理引擎到期提案建單（語意同 coinbase，全節點照單執行）。
		// M151c-v2：來源為治理金庫（treasury_pool）或創世社群基金（community_pool）或舊版獎勵池。
		treasuryFrom := t.FromAddr == chaindb.RewardPoolAddr || t.FromAddr == wallet.TreasuryAddr
		if !treasuryFrom && strings.HasPrefix(t.Memo, "gov:treasury:") {
			if communityPoolAddr != "" && t.FromAddr == communityPoolAddr {
				treasuryFrom = true
			}
		}
		if treasuryFrom && strings.HasPrefix(t.Memo, "gov:treasury:") {
			gAmt, gerr := parseFloat64(t.Amount)
			if gerr != nil || gAmt <= 0 {
				return fmt.Errorf("treasury 撥款金額非法: %s", t.Amount)
			}
			if t.ToAddr == "" {
				return errors.New("treasury 撥款收款人缺失")
			}
			continue
		}
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
		return errors.New("PoT 驗證失敗")
	}
	return nil
}

func parseFloat64(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
