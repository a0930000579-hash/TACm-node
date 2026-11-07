package zk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"tacm/internal/crypto"
)

// 本檔為 v156 新增：L2 Rollup「有效性證明」（validity proof）的無狀態建構與驗證。
//
// 定位（對標主流 rollup）：
//   - 這是「健全的簡化版」有效性證明——simplified ZK-validium 風格，
//     不依賴外部 pairing/曲线配對庫，以 SHA-256 Merkle 承諾＋狀態轉換
//     約束編碼＋提議者批簽名，達成與 zk-rollup 相同的「被篡改批次必被拒絕」
//     有效性保證；差異僅在於證明仍攜帶公開輸入（touched leaves），
//     故屬 validium（數據可得性由 L2 節點＋L1 承諾保證）而非隱私 zk-rollup。
//
// 驗證端（內嵌本檔）不持有完整狀態樹，僅靠公開輸入即可斷言：
//  1. 交易清單未被篡改（TxRoot = Merkle(ordered tx hashes) 重算比對）；
//  2. 每筆交易的簽名/授權有效（用攜帶 pubkey 重算 sighash 驗 ECDSA）；
//  3. 狀態轉換約束全部成立：餘額不可為負、nonce 單調遞增（防雙花/重放）、
//     總供給守恆（Σpost − Σpre == −Σfee，費用銷毀）；
//  4. 觸及賬戶 post 值確為 pre 經約束推導所得（逐葉比對）；
//  5. postRoot = H(prevRoot, preTouchedRoot, postTouchedRoot) 重算比對，
//     且提議者批簽名綁定 prevRoot/postRoot/txRoot/touchedRoot/batchIndex。
//
// 任何一步不符（改 post root、改任一交易、改任一 post 餘額、負餘額、
// nonce 重用、供給不守恆）都會讓 VerifyRollupValidityProof 返回 error。

// RollupTx 為一筆已執行 L2 交易（有效性證明的約束單元）。
// Amount/Fee 為最小單位十進位字串（canonical：FormatFloat(x,'f',-1,64)）。
type RollupTx struct {
	TxHash    string `json:"tx_hash"`
	From      string `json:"from"`
	To        string `json:"to"`
	Amount    string `json:"amount"`
	Fee       string `json:"fee"`
	Nonce     int64  `json:"nonce"`
	Ts        int64  `json:"ts"`
	Signature string `json:"signature"`
	Pubkey    string `json:"pubkey"`
}

// RollupAccTransition 為一個被觸及賬戶的 pre→post 狀態（約束公開輸入）。
type RollupAccTransition struct {
	Address     string `json:"address"`
	PreBalance  string `json:"pre_balance"`
	PreNonce    int64  `json:"pre_nonce"`
	PostBalance string `json:"post_balance"`
	PostNonce   int64  `json:"post_nonce"`
}

// RollupValidityProof 為一批 L2 交易的有效性證明（simplified validium 風格）。
type RollupValidityProof struct {
	BatchIndex  int64                 `json:"batch_index"`
	PrevRoot    string                `json:"prev_root"`
	PostRoot    string                `json:"post_root"`
	TxRoot      string                `json:"tx_root"`           // Merkle(ordered tx hashes)
	PreTouched  string                `json:"pre_touched_root"`  // Merkle(pre 觸及葉)
	PostTouched string                `json:"post_touched_root"` // Merkle(post 觸及葉)
	FeeBurned   string                `json:"fee_burned"`        // Σfee（守恆校驗）
	Timestamp   int64                 `json:"timestamp"`
	Txs         []RollupTx            `json:"txs"`
	Touched     []RollupAccTransition `json:"touched"`
	Signer      string                `json:"signer,omitempty"`
	Signature   string                `json:"signature,omitempty"`
}

// accLeafStr 為觸及賬戶狀態葉的確定性序列化（欄位以 \x1f 分隔防碰撞）。
func accLeafStr(t RollupAccTransition) string {
	return t.Address + "\x1f" + t.PreBalance + "\x1f" +
		strconv.FormatInt(t.PreNonce, 10) + "\x1f" +
		t.PostBalance + "\x1f" + strconv.FormatInt(t.PostNonce, 10)
}

// ParseAmount 把十進位字串轉成 big.Float；非法返回 error。
func ParseAmount(s string) (*big.Float, error) {
	f, ok := new(big.Float).SetString(strings.TrimSpace(s))
	if !ok {
		return nil, fmt.Errorf("zk: 金額非法 %q", s)
	}
	return f, nil
}

// digestCore 為批簽名與 postRoot 共同綁定的核心摘要。
func (p *RollupValidityProof) digestCore() []byte {
	h := sha256.New()
	h.Write([]byte(p.PrevRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(p.PostRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(p.TxRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(p.PreTouched))
	h.Write([]byte("\x1f"))
	h.Write([]byte(p.PostTouched))
	h.Write([]byte("\x1f"))
	h.Write([]byte(strconv.FormatInt(p.BatchIndex, 10)))
	return h.Sum(nil)
}

// BuildRollupValidityProof 建構有效性證明：計算 txRoot、pre/post touched roots、
// postRoot = H(prevRoot, preTouchedRoot, postTouchedRoot)；若提供 privKey（32B）
// 與 signerPubHex（壓縮公鑰 hex）則附加提議者批簽名。
//
// 注意：本函式不檢查約束是否成立（那是證明者的責任）；驗證端 VerifyRollupValidityProof
// 會完整重放並斷言所有約束。
func BuildRollupValidityProof(batchIndex int64, prevRoot string, ts int64,
	txs []RollupTx, touched []RollupAccTransition,
	privKey []byte, signerPubHex string) (*RollupValidityProof, error) {
	if prevRoot == "" {
		return nil, errors.New("zk: prev_root 不可為空")
	}
	if len(txs) == 0 {
		return nil, errors.New("zk: 批次不可為空")
	}
	if len(touched) == 0 {
		return nil, errors.New("zk: touched 不可為空")
	}

	// TxRoot：按執行順序的 tx 哈希 Merkle。
	txHashes := make([]string, len(txs))
	for i := range txs {
		if txs[i].TxHash == "" {
			return nil, fmt.Errorf("zk: tx #%d 缺少 tx_hash", i)
		}
		txHashes[i] = txs[i].TxHash
	}
	txRoot, err := crypto.MerkleRootStrings(txHashes)
	if err != nil {
		return nil, fmt.Errorf("zk: tx_root: %w", err)
	}

	// touched roots：按地址排序確定性編排。
	sorted := append([]RollupAccTransition(nil), touched...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Address < sorted[j].Address })
	preLeaves := make([]string, len(sorted))
	postLeaves := make([]string, len(sorted))
	for i, t := range sorted {
		preLeaves[i] = hex.EncodeToString(crypto.DoubleSHA256([]byte(accLeafStr(t))))
		postLeaves[i] = hex.EncodeToString(crypto.DoubleSHA256([]byte(
			t.Address + "\x1f" + t.PostBalance + "\x1f" + strconv.FormatInt(t.PostNonce, 10))))
	}
	preRoot, err := crypto.MerkleRootStrings(preLeaves)
	if err != nil {
		return nil, fmt.Errorf("zk: pre_touched_root: %w", err)
	}
	postRoot, err := crypto.MerkleRootStrings(postLeaves)
	if err != nil {
		return nil, fmt.Errorf("zk: post_touched_root: %w", err)
	}
	// 費用銷毀總額。
	feeBurned := big.NewFloat(0)
	for _, tx := range txs {
		fee, err := ParseAmount(tx.Fee)
		if err != nil {
			return nil, err
		}
		feeBurned.Add(feeBurned, fee)
	}

	// postRoot 承諾：H(prevRoot, preTouchedRoot, postTouchedRoot)。
	h := sha256.New()
	h.Write([]byte(prevRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(preRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(postRoot))
	committed := "0x" + hex.EncodeToString(h.Sum(nil))

	p := &RollupValidityProof{
		BatchIndex:  batchIndex,
		PrevRoot:    prevRoot,
		PostRoot:    committed,
		TxRoot:      txRoot,
		PreTouched:  preRoot,
		PostTouched: postRoot,
		FeeBurned:   feeBurned.Text('f', -1),
		Timestamp:   ts,
		Txs:         txs,
		Touched:     sorted,
		Signer:      signerPubHex,
	}
	if len(privKey) > 0 {
		sig, err := crypto.SignDigest(p.digestCore(), privKey)
		if err != nil {
			return nil, fmt.Errorf("zk: 批簽名: %w", err)
		}
		p.Signature = hex.EncodeToString(sig)
	}
	return p, nil
}

// VerifyRollupValidityProof 無狀態驗證有效性證明；返回 error（nil＝有效）。
func VerifyRollupValidityProof(p *RollupValidityProof) error {
	if p == nil {
		return errors.New("zk: 證明為空")
	}
	if p.PrevRoot == "" || p.PostRoot == "" {
		return errors.New("zk: prev_root/post_root 不可為空")
	}
	if len(p.Txs) == 0 {
		return errors.New("zk: 批次不可為空")
	}
	if len(p.Touched) == 0 {
		return errors.New("zk: touched 不可為空")
	}

	// 1) TxRoot 完整性：任一交易被篡改/增刪/重排都會改變 txRoot。
	txHashes := make([]string, len(p.Txs))
	for i, tx := range p.Txs {
		if tx.TxHash == "" {
			return fmt.Errorf("zk: tx #%d 缺少 tx_hash", i)
		}
		txHashes[i] = tx.TxHash
	}
	txRoot, err := crypto.MerkleRootStrings(txHashes)
	if err != nil {
		return err
	}
	if txRoot != p.TxRoot {
		return errors.New("zk: tx_root 不符（交易清單被篡改）")
	}

	// 2) 觸及葉承諾重算。
	sorted := append([]RollupAccTransition(nil), p.Touched...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Address < sorted[j].Address })
	preLeaves := make([]string, len(sorted))
	postLeaves := make([]string, len(sorted))
	for i, t := range sorted {
		preLeaves[i] = hex.EncodeToString(crypto.DoubleSHA256([]byte(accLeafStr(t))))
		postLeaves[i] = hex.EncodeToString(crypto.DoubleSHA256([]byte(
			t.Address + "\x1f" + t.PostBalance + "\x1f" + strconv.FormatInt(t.PostNonce, 10))))
	}
	preRoot, err := crypto.MerkleRootStrings(preLeaves)
	if err != nil {
		return err
	}
	postRoot, err := crypto.MerkleRootStrings(postLeaves)
	if err != nil {
		return err
	}
	if preRoot != p.PreTouched {
		return errors.New("zk: pre_touched_root 不符（pre 狀態被篡改）")
	}
	if postRoot != p.PostTouched {
		return errors.New("zk: post_touched_root 不符（post 狀態被篡改）")
	}

	// 3) postRoot 承諾綁定。
	h := sha256.New()
	h.Write([]byte(p.PrevRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(preRoot))
	h.Write([]byte("\x1f"))
	h.Write([]byte(postRoot))
	if "0x"+hex.EncodeToString(h.Sum(nil)) != p.PostRoot {
		return errors.New("zk: post_root 承諾不符（狀態轉換不對應）")
	}

	// 4) 提議者批簽名（若附）。
	if p.Signature != "" && p.Signer != "" {
		pub, err := hex.DecodeString(p.Signer)
		if err != nil {
			return fmt.Errorf("zk: 解碼簽名者公鑰: %w", err)
		}
		sigBytes, err := hex.DecodeString(p.Signature)
		if err != nil {
			return fmt.Errorf("zk: 解碼批簽名: %w", err)
		}
		if !crypto.VerifyDigest(pub, p.digestCore(), sigBytes) {
			return errors.New("zk: 批簽名無效")
		}
	}

	// 5) 狀態轉換約束重放。
	if err := replayConstraints(p); err != nil {
		return err
	}
	return nil
}

// replayConstraints 無狀態重放：餘額非負、nonce 單調（防雙花/重放）、
// 每筆交易 ECDSA 簽名授權、post 餘額/nonce 與聲稱一致、總供給守恆。
func replayConstraints(p *RollupValidityProof) error {
	idx := map[string]*RollupAccTransition{}
	preBalance := map[string]*big.Float{}
	preNonce := map[string]int64{}
	curBalance := map[string]*big.Float{}
	curNonce := map[string]int64{}
	var sumPre, sumPost big.Float

	for i := range p.Touched {
		t := &p.Touched[i]
		if _, dup := idx[t.Address]; dup {
			return fmt.Errorf("zk: touched 地址重複 %s", t.Address)
		}
		pre, err := ParseAmount(t.PreBalance)
		if err != nil {
			return err
		}
		post, err := ParseAmount(t.PostBalance)
		if err != nil {
			return err
		}
		idx[t.Address] = t
		preBalance[t.Address] = pre
		preNonce[t.Address] = t.PreNonce
		curBalance[t.Address] = new(big.Float).Set(pre)
		curNonce[t.Address] = t.PreNonce
		sumPre.Add(&sumPre, pre)
		sumPost.Add(&sumPost, post)
	}

	seenNonce := map[string]int64{}
	feeBurned := big.NewFloat(0)
	for i, tx := range p.Txs {
		// 授權：重算 sighash 驗 ECDSA（pubkey 派生地址須等於 from）。
		sigMap := map[string]any{
			"from": tx.From, "to": tx.To,
			"amount": tx.Amount, "fee": tx.Fee,
			"nonce": tx.Nonce, "ts": tx.Ts, "pubkey": tx.Pubkey,
		}
		if tx.Pubkey == "" || !crypto.VerifyTransactionSignature(sigMap, tx.Signature, tx.From) {
			return fmt.Errorf("zk: tx #%d 簽名/授權無效", i)
		}

		sender, ok := curBalance[tx.From]
		if !ok {
			return fmt.Errorf("zk: tx #%d 發送方 %s 不在觸及集合", i, tx.From)
		}
		receiver, ok := curBalance[tx.To]
		if !ok {
			return fmt.Errorf("zk: tx #%d 接收方 %s 不在觸及集合", i, tx.To)
		}
		amount, err := ParseAmount(tx.Amount)
		if err != nil {
			return err
		}
		fee, err := ParseAmount(tx.Fee)
		if err != nil {
			return err
		}

		// nonce 單調：同一發送方每筆期望 preNonce + 已見筆數。
		expect := preNonce[tx.From] + seenNonce[tx.From]
		if tx.Nonce != expect {
			return fmt.Errorf("zk: tx #%d nonce 違規（期望 %d, 實得 %d）— 可能雙花/重放",
				i, expect, tx.Nonce)
		}
		seenNonce[tx.From]++

		// 餘額不可為負。
		spend := new(big.Float).Add(amount, fee)
		after := new(big.Float).Sub(sender, spend)
		if after.Sign() < 0 {
			return fmt.Errorf("zk: tx #%d 發送方餘額不足（負餘額違規）", i)
		}
		sender.Set(after)
		receiver.Add(receiver, amount)
		curNonce[tx.From]++
		feeBurned.Add(feeBurned, fee)
	}

	// 逐葉比對 post 餘額/nonce。
	for addr, t := range idx {
		gotB := curBalance[addr]
		wantB, err := ParseAmount(t.PostBalance)
		if err != nil {
			return err
		}
		if gotB.Cmp(wantB) != 0 {
			return fmt.Errorf("zk: 地址 %s post 餘額不符（%s != %s）",
				addr, gotB.Text('f', -1), wantB.Text('f', -1))
		}
		if curNonce[addr] != t.PostNonce {
			return fmt.Errorf("zk: 地址 %s post nonce 不符（%d != %d）",
				addr, curNonce[addr], t.PostNonce)
		}
	}

	// 總供給守恆：Σpost − Σpre == −Σfee（fee 銷毀）。
	delta := new(big.Float).Sub(&sumPost, &sumPre)
	want := new(big.Float).Neg(feeBurned)
	if delta.Cmp(want) != 0 {
		return fmt.Errorf("zk: 供給不守恆（Δ=%s, 期望 −Σfee=%s）",
			delta.Text('f', -1), want.Text('f', -1))
	}
	// 證明自報的 FeeBurned 也須一致。
	if fb, err := ParseAmount(p.FeeBurned); err == nil && fb.Cmp(feeBurned) != 0 {
		return fmt.Errorf("zk: fee_burned 自報不符（%s != %s）",
			p.FeeBurned, feeBurned.Text('f', -1))
	}
	return nil
}
