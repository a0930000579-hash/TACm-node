package node

import (
	"fmt"

	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// tacAssetSettler M150e：以錢包帳本 + 金庫地址實作橋的目標鏈資產結算。
// 鎖定（TAC→BSC）：用戶 TACM → 金庫；釋放（BSC→TAC）：金庫 → 目標地址。
// 使用 InternalTransfer（橋單已扣手續費，結算階段不再重複收費）。
type tacAssetSettler struct {
	walletSvc *wallet.Service
	vault     string // 橋金庫 TAC 地址
}

// LockOnSource 源鏈鎖定：從用戶地址扣 TACM 入金庫。
func (s *tacAssetSettler) LockOnSource(sourceAddr string, amount float64) error {
	if s.vault == "" {
		return fmt.Errorf("bridge 金庫地址未設定")
	}
	if amount <= 0 {
		return fmt.Errorf("鎖定金額必須大於 0: %g", amount)
	}
	a := wallet.NewAmountF(amount, wallet.AssetTACm)
	if a.IsZero() {
		return fmt.Errorf("鎖定金額轉換為 0: %g", amount)
	}
	return s.walletSvc.InternalTransfer(sourceAddr, s.vault, wallet.AssetTACm, a, "bridge:lock")
}

// ReleaseOnTarget 目標鏈釋放：從金庫轉 TACM 給目標地址。
func (s *tacAssetSettler) ReleaseOnTarget(targetAddr string, amount float64) error {
	if s.vault == "" {
		return fmt.Errorf("bridge 金庫地址未設定")
	}
	if amount <= 0 {
		return fmt.Errorf("釋放金額必須大於 0: %g", amount)
	}
	a := wallet.NewAmountF(amount, wallet.AssetTACm)
	if a.IsZero() {
		return fmt.Errorf("釋放金額轉換為 0: %g", amount)
	}
	return s.walletSvc.InternalTransfer(s.vault, targetAddr, wallet.AssetTACm, a, "bridge:unlock")
}

// AttachBridgeAssetSettler 為橋掛載 TAC 側資產結算器（金庫模型）。
// vaultAddr 為橋金庫地址；空字串時回退到確定性派生的專用金庫地址
// （隔離於節點地址與獎勵池，符合主流鏈 Lock&Mint escrow 原則）。
func (n *Node) AttachBridgeAssetSettler(vaultAddr string) error {
	if n.bridge == nil {
		return fmt.Errorf("橋未啟用（-bridge）")
	}
	if n.walletSvc == nil {
		return fmt.Errorf("錢包服務未啟用")
	}
	if vaultAddr == "" {
		vaultAddr = DefaultBridgeVaultAddr()
	}
	n.bridge.SetAssetSettler(&tacAssetSettler{walletSvc: n.walletSvc, vault: vaultAddr})
	return nil
}

// DefaultBridgeVaultAddr 傳回橋金庫的確定性派生地址：由固定域分離 seed
// 派生 hash160 → tx0 base58check。地址可預測、可審計（金庫餘額＝淨鎖定量）；
// 帳本層由節點守衛控制，未來如引入鏈上跨鏈交易可將該地址作為合約託管方。
func DefaultBridgeVaultAddr() string {
	h := crypto.SHA256([]byte("tacm:bridge:vault:v1"))
	return crypto.Hash160ToAddress(h[:20])
}
