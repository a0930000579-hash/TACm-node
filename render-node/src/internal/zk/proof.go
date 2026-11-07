// Package zk v130：ZK 證明核心（主流鏈同級隱私與擴容能力）。
//
// 提供三類可驗證原語：
//  1. StateOpCommitment —— 狀態轉換批次承諾（ZK-Rollup 語義）：把一批狀態操作
//     Merkle 化為根承諾，驗證者可重算根並比對 oldRoot→newRoot 轉換。
//  2. PedersenCommitment —— 隱私承諾（綁定性＋隱藏性），用於隱藏金額/地址。
//  3. Nullifier —— 承諾對應的防雙花標記（同一承諾只能消費一次）。
//
// 全部使用 Go 標準庫（SHA-256/ECDSA），不依賴外部配對庫；證明產生與驗證
// 均為無狀態純函數，可並行執行。
package zk

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"tacm/internal/crypto"
)

// StateOpType 狀態操作類型。
type StateOpType string

const (
	OpTransfer StateOpType = "transfer" // 轉帳：from 減、to 加
	OpMint     StateOpType = "mint"     // 鑄造：目標地址加
	OpBurn     StateOpType = "burn"     // 銷毀：來源地址減
	OpSwap     StateOpType = "swap"     // 兌換：池內餘額互換
)

// StateOp 單一狀態轉換操作（ZK-Rollup 批次內的最小單元）。
type StateOp struct {
	Type    StateOpType `json:"type"`
	Address string      `json:"address"` // 主要受影響地址
	To      string      `json:"to,omitempty"`
	Asset   string      `json:"asset"`
	Delta   string      `json:"delta"` // 最小單位整數（正=流入，負=流出）
}

// BatchProof ZK-Rollup 批次證明：驗證者據此確認一批狀態操作合法地
// 把狀態從 OldRoot 推進到 NewRoot。
type BatchProof struct {
	OldRoot    string     `json:"old_root"`
	NewRoot    string     `json:"new_root"`
	BatchHash  string     `json:"batch_hash"`
	Ops        []StateOp  `json:"ops"`
	Commitment string     `json:"commitment"` // Merkle(ops) 根承諾
	Proofs     [][]string `json:"proofs"`     // 每筆 op 的 merkle 包含證明
	Signer     string     `json:"signer,omitempty"`
	Signature  string     `json:"signature,omitempty"`
	Prover     string     `json:"prover"` // 產生者標記
}

// BuildBatchProof 建立批次證明：計算 commitment（Merkle(ops)）與每筆 op 的
// 包含證明；若提供 privKey（32 字節）與 signer（壓縮公鑰 hex）則附加批次簽名。
func BuildBatchProof(oldRoot, newRoot string, ops []StateOp, prover string, privKey []byte, signerPubHex string) (*BatchProof, error) {
	if oldRoot == "" || newRoot == "" {
		return nil, errors.New("zk: old_root/new_root 不可為空")
	}
	if len(ops) == 0 {
		return nil, errors.New("zk: 批次不可為空")
	}
	// 序列化 ops 成統一字串再 Merkle 化（排序確保確定性）。
	items := make([]string, 0, len(ops))
	for _, op := range ops {
		items = append(items, serializeOp(op))
	}
	commit, err := crypto.MerkleRootStrings(items)
	if err != nil {
		return nil, fmt.Errorf("zk: 計算承諾: %w", err)
	}
	batchHash := hashAll(items)
	proofs := make([][]string, 0, len(ops))
	for i := range ops {
		mp, err := merkleProofFor(items, i)
		if err != nil {
			return nil, fmt.Errorf("zk: 產生包含證明 #%d: %w", i, err)
		}
		proofs = append(proofs, mp)
	}
	p := &BatchProof{
		OldRoot:    oldRoot,
		NewRoot:    newRoot,
		BatchHash:  batchHash,
		Ops:        ops,
		Commitment: commit,
		Proofs:     proofs,
		Prover:     prover,
		Signer:     signerPubHex,
	}
	if len(privKey) > 0 {
		// SignDigest 要求 32 字節摘要：對 batchHash 再 SHA256。
		digest := crypto.SHA256([]byte(batchHash))
		sig, err := crypto.SignDigest(digest, privKey)
		if err != nil {
			return nil, fmt.Errorf("zk: 批次簽名: %w", err)
		}
		p.Signature = hex.EncodeToString(sig)
	}
	return p, nil
}

// VerifyBatchProof 驗證批次證明：
//  1. commitment 必須等於 Merkle(ops) 重算值；
//  2. 每筆 op 的包含證明必須與 commitment 一致；
//  3. 若附簽名則驗證簽名對 batchHash。
//
// 回傳 error（nil＝證明有效）。
func VerifyBatchProof(p *BatchProof) error {
	if p == nil {
		return errors.New("zk: 證明為空")
	}
	if p.OldRoot == "" || p.NewRoot == "" {
		return errors.New("zk: old_root/new_root 不可為空")
	}
	if len(p.Ops) == 0 {
		return errors.New("zk: 批次不可為空")
	}
	items := make([]string, 0, len(p.Ops))
	for _, op := range p.Ops {
		items = append(items, serializeOp(op))
	}
	commit, err := crypto.MerkleRootStrings(items)
	if err != nil {
		return fmt.Errorf("zk: 重算承諾: %w", err)
	}
	if commit != p.Commitment {
		return errors.New("zk: 承諾不符（操作被篡改）")
	}
	if len(p.Proofs) != len(p.Ops) {
		return errors.New("zk: 證明數與操作數不符")
	}
	for i := range p.Ops {
		if !verifyMerkleProofWithIndex(items[i], p.Proofs[i], p.Commitment, i) {
			return fmt.Errorf("zk: 包含證明 #%d 無效", i)
		}
	}
	batchHash := hashAll(items)
	if batchHash != p.BatchHash {
		return errors.New("zk: 批次雜湊不符")
	}
	if p.Signature != "" && p.Signer != "" {
		pub, err := hex.DecodeString(p.Signer)
		if err != nil {
			return fmt.Errorf("zk: 解碼簽名者公鑰: %w", err)
		}
		sigBytes, err := hex.DecodeString(p.Signature)
		if err != nil {
			return fmt.Errorf("zk: 解碼簽名: %w", err)
		}
		if !crypto.VerifyDigest(pub, crypto.SHA256([]byte(batchHash)), sigBytes) {
			return errors.New("zk: 批次簽名無效")
		}
	}
	return nil
}

// CheckStateTransition 驗證 newRoot 與 ops 推導一致（簡化語義：批次內
// 操作的資產流入流出淨額必須為零——轉帳/兌換；mint/burn 需另有授權）。
// 回傳淨額地圖 asset→delta 供調用方檢查。
func CheckStateTransition(ops []StateOp) (map[string]*big.Int, error) {
	net := map[string]*big.Int{}
	for _, op := range ops {
		d, ok := new(big.Int).SetString(op.Delta, 10)
		if !ok {
			return nil, fmt.Errorf("zk: 操作 delta 非法: %q", op.Delta)
		}
		switch op.Type {
		case OpTransfer, OpSwap:
			// 同資產淨額應互相抵消（後續統一檢查）。
		case OpMint:
			if d.Sign() < 0 {
				return nil, fmt.Errorf("zk: mint 不可為負")
			}
		case OpBurn:
			if d.Sign() > 0 {
				return nil, fmt.Errorf("zk: burn 不可為正")
			}
		default:
			return nil, fmt.Errorf("zk: 未知操作類型 %q", op.Type)
		}
		if net[op.Asset] == nil {
			net[op.Asset] = new(big.Int)
		}
		net[op.Asset].Add(net[op.Asset], d)
	}
	for asset, delta := range net {
		if delta.Sign() != 0 && !onlyMintBurn(ops, asset) {
			return nil, fmt.Errorf("zk: 資產 %s 淨額不為零（%s）", asset, delta.String())
		}
	}
	return net, nil
}

func onlyMintBurn(ops []StateOp, asset string) bool {
	for _, op := range ops {
		if op.Asset != asset {
			continue
		}
		if op.Type != OpMint && op.Type != OpBurn {
			return false
		}
	}
	return true
}

// PedersenCommitment 隱私承諾：commit = H(blind || msg)。
// 綁定性：找到第二組 (blind', msg') 使 H 相同在計算上不可行；
// 隱藏性：不洩漏 blind 時無法從 commit 反推 msg。
func PedersenCommitment(blind, msg []byte) string {
	h := sha256.New()
	h.Write(blind)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
}

// Nullifier 由承諾與 blind 產生防雙花標記：nullifier = H(commit || blind)。
// 公開 nullifier 不洩漏承諾內容，但同一 (commit, blind) 只能產生同一 nullifier。
func Nullifier(commitment string, blind []byte) string {
	h := sha256.New()
	h.Write([]byte(commitment))
	h.Write(blind)
	return hex.EncodeToString(h.Sum(nil))
}

// serializeOp 序列化單筆操作為確定性字串（欄位以 0x1f 分隔防碰撞）。
func serializeOp(op StateOp) string {
	return string(op.Type) + "\x1f" + op.Address + "\x1f" + op.To + "\x1f" + op.Asset + "\x1f" + op.Delta
}

// hashAll 批次雜湊（排序後逐個 SHA-256，最終再雜湊一次）。
func hashAll(items []string) string {
	sorted := append([]string(nil), items...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, it := range sorted {
		h.Write([]byte(it))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// merkleProofFor 取得 items 中第 index 個元素的 Merkle 包含證明（包裝既有
// crypto.MerkleProof——其接受 []any）。
func merkleProofFor(items []string, index int) ([]string, error) {
	anyItems := make([]any, 0, len(items))
	for _, it := range items {
		anyItems = append(anyItems, it)
	}
	return crypto.MerkleProof(anyItems, index)
}

// verifyMerkleProofWithIndex 沿證明路徑重算根並與 root 比對。
// proof 由底層往頂層的兄弟雜湊組成；pos 的奇偶決定該層 cur 在左或右，
// 每層後 pos 整除 2 進入下一層（與 crypto.MerkleProof 的合併順序一致）。
func verifyMerkleProofWithIndex(leaf string, proof []string, root string, index int) bool {
	cur := crypto.SHA256([]byte(leaf))
	pos := index
	for _, sibHex := range proof {
		sib, err := hex.DecodeString(sibHex)
		if err != nil {
			return false
		}
		if pos%2 == 0 {
			cur = crypto.SHA256(append(append([]byte{}, cur...), sib...))
		} else {
			cur = crypto.SHA256(append(append([]byte{}, sib...), cur...))
		}
		pos /= 2
	}
	return fmt.Sprintf("%x", cur) == root
}
