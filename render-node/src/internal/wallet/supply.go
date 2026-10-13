package wallet

import (
	"fmt"
	"math"
)

// M35：TiUSD 穩定幣錨定規則（防幣值崩盤）。
//
// 規則（用戶設定）：
//  1. 最小流通量＝TACm 總供應量（TACM_MAX_SUPPLY）之 0.330 倍（TiUSDFloorBps=3300）；
//     銷毀（burn）後不得低於此下限，低於下限時由供給層自動補鑄至下限。
//  2. 最大流通量＝TACm 總供應量（1:1 錨定上限）；增發（mint）後不得超過此上限。
//  3. 未設定 TACM_MAX_SUPPLY（總供應上限=0，減半模式）時不啟用上下限（向後相容）。
const TiUSDFloorBps = 3300

// SetTACMSupplyCap 由節點注入 TACm 總供應上限（TACM_MAX_SUPPLY 環境變數解析結果）。
func (s *Service) SetTACMSupplyCap(maxSupply float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxSupply > 0 {
		s.tacmMaxSupply = maxSupply
	}
}

// tiUSDSupplies 回傳 TiUSD 流通上下限（最小單位）。
// 上限＝TACm 總供應量（1:1）；下限＝總供應量 × 0.330。
func (s *Service) tiUSDSupplies() (floor, cap int64, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tacmMaxSupply <= 0 {
		return 0, 0, false
	}
	// TiUSD 以 1e6（micro）為最小單位且 int64 承載——總供應 52,003,300×1e6＝5.2e13 在 int64 安全範圍。
	cap = int64(math.Round(s.tacmMaxSupply * 1e6)) // 總供應量 1:1 錨定（1e6 最小單位）
	floor = cap * TiUSDFloorBps / 10000            // 總供應 × 0.330
	return floor, cap, true
}

// EnsureTiUSDFloor 開鏈/重啟後檢查 TiUSD 流通量，低於下限時由供給層自動補鑄至下限
// （入帳至獎勵池＝去中心化交易所資金池，維持最低發行量）。
// 回傳本次補鑄數量（最小單位）；已達標回傳 0。
func (s *Service) EnsureTiUSDFloor() (int64, error) {
	floor, _, enabled := s.tiUSDSupplies()
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
