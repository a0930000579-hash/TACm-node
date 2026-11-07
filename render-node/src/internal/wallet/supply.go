package wallet

import (
	"fmt"
	"math"
	"math/big"
)

// M35＋v102：TiUSD 穩定幣錨定規則（防幣值崩盤）。
//
// 規則（用戶設定 v102 更新）：
//  1. TiUSD 最大流通量＝TACm 市值（1:1 市值錨定）＝TACm 總發行量 × TACm 市價（最後成交價）。
//     上市（有市價）：市值縮水 → 供給層自動銷毀至新上限（供應不超額膨脹）。
//  2. 未上市（無成交價）：按 1:0.330 計算（上限＝TACm 總發行量 × 0.330）。
//  3. 最低流通支撐（floor）＝ TACm 總發行量 × 0.330；市值上限低於此值時以市值為準。
//  4. 未設定 TACM_MAX_SUPPLY（總供應上限=0，減半模式）時不啟用上下限（向後相容）。
const TiUSDFloorBps = 3300

// SetTACMSupplyCap 由節點注入 TACm 總供應上限（TACM_MAX_SUPPLY 環境變數解析結果）。
func (s *Service) SetTACMSupplyCap(maxSupply float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxSupply > 0 {
		s.tacmMaxSupply = maxSupply
	}
}

// SetTACMPrice v102：注入 TACm 市價（USDT/TACm，最後成交價；0＝未上市）。
// 由節點在每次成交後更新；無成交時維持 0（上限計算以 0.330 兜底）。
func (s *Service) SetTACMPrice(price float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if price >= 0 {
		s.tacmPrice = price
	}
}

// TACMPrice v102：當前注入市價（0＝未上市）。
func (s *Service) TACMPrice() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tacmPrice
}

// TACMSupplyCap v101：公開 TACm 總供應上限（TACM_MAX_SUPPLY；0＝未設定，減半模式）。
func (s *Service) TACMSupplyCap() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tacmMaxSupply
}

// TiUSDCap v100/v102：公開供應上限/下限（TiUSD 監控台用）。enabled=false 表示未設 TACM_MAX_SUPPLY（上限未啟用）。
func (s *Service) TiUSDCap() (floor, capAmt int64, enabled bool, err error) {
	return s.tiUSDSupplies()
}

// tiUSDSupplies 回傳 TiUSD 流通上下限（最小單位）。
//   - 上限（cap）＝TACm 市值：上市（有市價）＝TACm 發行量 × 市價；未上市＝TACm 發行量 × 0.330。
//   - 下限（floor）＝TACm 發行量 × 0.330（最低流通支撐）；市值上限低於此值時以市值為準。
func (s *Service) tiUSDSupplies() (floor, capAmt int64, enabled bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tacmMaxSupply <= 0 {
		return 0, 0, false, nil
	}
	mined, merr := s.st.ChainMinedTacm()
	if merr != nil {
		return 0, 0, false, fmt.Errorf("wallet: 讀取 TACm 發行量: %w", merr)
	}
	// v102-fix：鏈上尚未出塊（mined=0）時不啟用錨定——避免創世種子注資被 cap=0 擋死。
	if mined.Sign() <= 0 {
		return 0, 0, false, nil
	}
	// 市值上限（TiUSD micro）＝ TACm 發行量(1e18) × 市價(USDT/TACm) × 1e6 / 1e18。
	price := s.tacmPrice
	if price <= 0 {
		price = 0.330 // 未上市兜底
	}
	priceMicro := int64(math.Round(price * 1e6))
	capBI := new(big.Int).Mul(mined, big.NewInt(priceMicro))
	capBI.Div(capBI, big.NewInt(1e18))
	// 下限＝發行量 × 0.330（micro）；不超過市值上限。
	floorBI := new(big.Int).Mul(mined, big.NewInt(int64(TiUSDFloorBps)))
	floorBI.Div(floorBI, big.NewInt(1e16))
	if floorBI.Cmp(capBI) > 0 {
		floorBI.Set(capBI)
	}
	return floorBI.Int64(), capBI.Int64(), true, nil
}

// EnsureTiUSDFloor 開鏈/重啟後檢查 TiUSD 流通量，低於下限時由供給層自動補鑄至下限
// （入帳至獎勵池＝去中心化交易所資金池，維持最低發行量）。
// 回傳本次補鑄數量（最小單位）；已達標回傳 0。
func (s *Service) EnsureTiUSDFloor() (int64, error) {
	floor, _, enabled, err := s.tiUSDSupplies()
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}
	sup, err := s.st.TiUSDSummary()
	if err != nil {
		return 0, fmt.Errorf("wallet: 讀取 TiUSD 供給: %w", err)
	}
	if sup.Supply >= floor {
		return 0, nil
	}
	diff := floor - sup.Supply
	if _, err := s.MintTiUSD(RewardPoolAddr, diff, "supply:floor-topup"); err != nil {
		return 0, fmt.Errorf("wallet: TiUSD 補鑄至下限: %w", err)
	}
	return diff, nil
}

// EnsureTiUSDCap v102：市值上限自動銷毀——流通 > 市值上限時銷毀至上限
// （TACm 市值縮水 → TiUSD 自動縮減，確保不超額膨脹）。memo＝supply:cap-burn。
// 回傳本次銷毀數量（最小單位）；已達標回傳 0。
func (s *Service) EnsureTiUSDCap() (int64, error) {
	_, capAmt, enabled, err := s.tiUSDSupplies()
	if err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}
	sup, err := s.st.TiUSDSummary()
	if err != nil {
		return 0, fmt.Errorf("wallet: 讀取 TiUSD 供給: %w", err)
	}
	if sup.Supply <= capAmt {
		return 0, nil
	}
	diff := sup.Supply - capAmt
	// 與 BurnTiUSD 一致：僅調整供給層（total_burned 累加），不寫帳戶 ledger（無對應餘額變動）。
	if err := s.st.adjustTiUSD(-diff, 0, diff); err != nil {
		return 0, fmt.Errorf("wallet: 市值銷毀: %w", err)
	}
	return diff, nil
}
