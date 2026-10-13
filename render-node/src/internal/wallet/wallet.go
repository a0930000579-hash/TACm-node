package wallet

import (
	"fmt"
	"math/big"
	"sync"
)

// Service 為錢包高層操作：多資產轉帳（含手續費）、TiUSD 發行/銷毀、
// 充值/提現/獎勵入帳。所有操作走單一事務 + 雙分錄審計。
type Service struct {
	st            *Store
	mu            sync.Mutex
	tacmMaxSupply float64 // M35：TACm 總供應上限（TiUSD 錨定基準）
}

// NewService 創建錢包服務。
func NewService(st *Store) *Service { return &Service{st: st} }

// Store 返回底層帳本（供節點掛鉤/測試）。
func (s *Service) Store() *Store { return s.st }

// feeAmount 返回手續費 Amount（與資產匹配）。
func feeAmount(asset Asset, amount Amount) Amount {
	if asset == AssetTACm {
		return Amount{Big: FeeOf(amount.Big, asset.FeeBps())}
	}
	return Amount{I64: FeeOf(big.NewInt(amount.I64), asset.FeeBps()).Int64()}
}

// Transfer 站內轉帳：from → to，金額為最小單位；手續費按資產費率先行扣除
// （TiUSD 50bp / USDT 125bp / TACm 200bp），手續費入「fee」帳戶。
// 返回手續費金額。
func (s *Service) Transfer(from, to string, asset Asset, amount Amount, memo string) (Amount, error) {
	if from == "" || to == "" {
		return Amount{}, fmt.Errorf("wallet: 轉帳地址不能為空")
	}
	if from == to {
		return Amount{}, fmt.Errorf("wallet: 轉帳方與接收方相同")
	}
	if amount.Cmp(Amount{}) <= 0 {
		return Amount{}, fmt.Errorf("wallet: 轉帳金額必須大於 0")
	}
	if !asset.Valid() {
		return Amount{}, fmt.Errorf("wallet: 不支持的資產 %s", asset)
	}
	fee := feeAmount(asset, amount)
	if asset == AssetTACm {
		total := new(big.Int).Add(amount.Big, fee.Big)
		entries := []LedgerEntry{
			Entry(KindTransferOut, from, asset, new(big.Int).Neg(total), memo),
			Entry(KindTransferIn, to, asset, amount, memo),
			Entry(KindFee, "fee", asset, fee, memo),
		}
		if err := s.st.ApplyEntries(entries); err != nil {
			return Amount{}, err
		}
		return fee, nil
	}
	entries := []LedgerEntry{
		Entry(KindTransferOut, from, asset, -(amount.I64 + fee.I64), memo),
		Entry(KindTransferIn, to, asset, amount.I64, memo),
		Entry(KindFee, "fee", asset, fee.I64, memo),
	}
	if err := s.st.ApplyEntries(entries); err != nil {
		return Amount{}, err
	}
	return fee, nil
}

// InternalTransfer 內部移轉（資金池/費用結算等系統內部用途）：與 Transfer 相同但不收取手續費。
// 僅供服務端內部記帳調用（社群市集鏈上費結算、資金池 topup 等），不對用戶暴露。
func (s *Service) InternalTransfer(from, to string, asset Asset, amount Amount, memo string) error {
	if from == "" || to == "" {
		return fmt.Errorf("wallet: 轉帳地址不能為空")
	}
	if from == to {
		return fmt.Errorf("wallet: 轉帳方與接收方相同")
	}
	if amount.Cmp(Amount{}) <= 0 {
		return fmt.Errorf("wallet: 轉帳金額必須大於 0")
	}
	if !asset.Valid() {
		return fmt.Errorf("wallet: 不支持的資產 %s", asset)
	}
	if asset == AssetTACm {
		return s.st.ApplyEntries([]LedgerEntry{
			Entry(KindTransferOut, from, asset, new(big.Int).Neg(amount.Big), memo),
			Entry(KindTransferIn, to, asset, amount.Big, memo),
		})
	}
	return s.st.ApplyEntries([]LedgerEntry{
		Entry(KindTransferOut, from, asset, -amount.I64, memo),
		Entry(KindTransferIn, to, asset, amount.I64, memo),
	})
}

// Deposit 充值入帳（外部來源：鏈上充值/兌入）——打破守恆（外部資金）。
func (s *Service) Deposit(address string, asset Asset, amount Amount, memo string) error {
	if address == "" || amount.Cmp(Amount{}) <= 0 || !asset.Valid() {
		return fmt.Errorf("wallet: 充值參數非法 (address=%q amount=%s asset=%s)", address, amount.String(asset), asset)
	}
	return s.st.ApplyEntries([]LedgerEntry{
		Entry(KindDeposit, address, asset, amount, memo),
	})
}

// Reward 獎勵入帳（挖礦分發/獎勵池）——打破守恆（鑄造來源）。
func (s *Service) Reward(address string, asset Asset, amount Amount, memo string) error {
	if address == "" || amount.Cmp(Amount{}) <= 0 || !asset.Valid() {
		return fmt.Errorf("wallet: 獎勵參數非法")
	}
	return s.st.ApplyEntries([]LedgerEntry{
		Entry(KindReward, address, asset, amount, memo),
	})
}

// Withdraw 站內提現：錢包餘額 → 鏈上地址（TACm）。
// 視為站內轉帳（鏈上收帳方為外部地址帳戶），手續費同上；返回手續費。
func (s *Service) Withdraw(from, toChainAddr string, asset Asset, amount Amount, memo string) (Amount, error) {
	if asset != AssetTACm {
		return Amount{}, fmt.Errorf("wallet: 鏈上提現僅支持 TACm（%s 請用站內轉帳）", asset)
	}
	return s.Transfer(from, toChainAddr, asset, amount, memo)
}

// MintTiUSD 發行 TiUSD 穩定幣：供給增加，並入帳指定地址（peg 1 USDT）。
// 返回新供給。
func (s *Service) MintTiUSD(address string, amount int64, note string) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("wallet: TiUSD 發行量必須大於 0")
	}
	_, capAmt, enabled := s.tiUSDSupplies()
	if enabled {
		sup0, err0 := s.st.TiUSDSummary()
		if err0 != nil {
			return 0, fmt.Errorf("wallet: 讀取 TiUSD 供給: %w", err0)
		}
		if sup0.Supply+amount > capAmt {
			return 0, fmt.Errorf("wallet: TiUSD 發行超上限（總供應 1:1 錨定，當前 %d 最小單位）", capAmt)
		}
	}
	if err := s.st.adjustTiUSD(amount, amount, 0); err != nil {
		return 0, err
	}
	if err := s.st.ApplyEntries([]LedgerEntry{
		Entry(KindMint, address, AssetTiUSD, amount, note),
	}); err != nil {
		_ = s.st.adjustTiUSD(-amount, 0, amount)
		return 0, err
	}
	sup, err := s.st.TiUSDSummary()
	if err != nil {
		return 0, err
	}
	return sup.Supply, nil
}

// BurnTiUSD 銷毀 TiUSD：供給減少（不回帳戶餘額，僅供給層銷毀）。
func (s *Service) BurnTiUSD(amount int64, note string) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("wallet: TiUSD 銷毀量必須大於 0")
	}
	floorAmt, _, enabled := s.tiUSDSupplies()
	if enabled {
		sup0, err0 := s.st.TiUSDSummary()
		if err0 != nil {
			return 0, fmt.Errorf("wallet: 讀取 TiUSD 供給: %w", err0)
		}
		if sup0.Supply-amount < floorAmt {
			return 0, fmt.Errorf("wallet: TiUSD 銷毀將低於最小流通量（總供應 0.330 倍＝%d 最小單位）", floorAmt)
		}
	}
	if err := s.st.adjustTiUSD(-amount, 0, amount); err != nil {
		return 0, err
	}
	sup, err := s.st.TiUSDSummary()
	if err != nil {
		return 0, err
	}
	return sup.Supply, nil
}

// Balance 返回帳戶快照。
func (s *Service) Balance(address string) (*Account, error) {
	return s.st.Balance(address)
}

// Ledger 返回審計流。
func (s *Service) Ledger(limit int) ([]LedgerEntry, error) {
	return s.st.Ledger(limit)
}

// TiUSDSummary 返回穩定幣供給摘要。
func (s *Service) TiUSDSummary() (*TiUSDSupply, error) {
	return s.st.TiUSDSummary()
}
