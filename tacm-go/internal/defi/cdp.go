package defi

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// CDP 常數（M149s：TiUSD 超額抵押真實化——DAI 式）。
const (
	CDPAccount      = "defi_cdp"  // 抵押品託管帳戶（wallet 帳本）
	CDPMinRatio     = 1.50        // 最低抵押率 150%（超額抵押）
	CDPLiqRatio     = 1.30        // 清算線 130%（跌破可清算）
	CDPLiqPenalty   = 0.05        // 清算罰金 5%（給清算人）
	CDPStabilityAPR = 0.02        // 穩定費年息 2%（DAI 式 stability fee）
	CDPStatusOpen   = "open"
	CDPStatusClosed = "closed"
	CDPStatusLiq    = "liquidated"
)

// CDP 為 DAI 式抵押債務頭寸（Collateralized Debt Position）：
// 抵押 TACm → 借出 TiUSD；抵押率 = 抵押品市值 / 債務；跌破清算線可被任何人清算。
type CDP struct {
	ID         int64   `json:"id"`
	Owner      string  `json:"owner"`
	Collateral float64 `json:"collateral"`    // 抵押 TACm
	Debt       float64 `json:"debt"`          // 債務 TiUSD（含應計穩定費）
	FeeAccrued float64 `json:"fee_accrued"`   // 已應計未償穩定費
	LiqPrice   float64 `json:"liq_price"`     // 清算價格（TACm 跌至此即清算）
	OpenedAt   int64   `json:"opened_at"`
	UpdatedAt  int64   `json:"updated_at"`
	Status     string  `json:"status"`        // open / closed / liquidated
}

// CDPMarket 為 CDP 市場總覽。
type CDPMarket struct {
	TotalCollateral  float64 `json:"total_collateral"`   // 全市場抵押 TACm
	TotalDebt        float64 `json:"total_debt"`         // 全市場債務 TiUSD
	AvgRatio         float64 `json:"avg_ratio"`          // 平均抵押率
	OpenPositions    int     `json:"open_positions"`     // 未平倉數
	Liquidatable     int     `json:"liquidatable"`       // 可清算倉位數
	PriceUSD         float64 `json:"price_usd"`          // 當前 TACm 參考價
	MinRatio         float64 `json:"min_ratio"`
	LiqRatio         float64 `json:"liq_ratio"`
	LiqPenalty       float64 `json:"liq_penalty"`
	StabilityAPR     float64 `json:"stability_apr"`
	PriceConfigured  bool    `json:"price_configured"`   // 價格是否已配置（未配置時 CDP 暫停）
}

// OpenCDP 開倉：抵押 TACm → 借出 TiUSD（DAI 式）。
// priceUSD 為 TACm 參考價（USD）；抵押率 = collateral*priceUSD / borrow ≥ 150%。
func (s *Store) OpenCDP(owner string, collateral, borrow, priceUSD float64) (int64, error) {
	if owner == "" {
		return 0, errors.New("cdp: 地址為空")
	}
	if priceUSD <= 0 {
		return 0, errors.New("cdp: TACm 參考價未配置，暫停開倉")
	}
	if collateral <= 0 || borrow <= 0 {
		return 0, errors.New("cdp: 抵押品與借款金額必須大於 0")
	}
	ratio := (collateral * priceUSD) / borrow
	if ratio < CDPMinRatio-1e-9 {
		return 0, fmt.Errorf("cdp: 抵押率 %.2f 低於最低要求 %.2f", ratio, CDPMinRatio)
	}
	now := time.Now().Unix()
	liqPrice := CDPLiqRatio * borrow / collateral
	res, err := s.db.Exec(`INSERT INTO defi_cdps(owner, collateral, debt, fee_accrued, liq_price, status, opened_at, updated_at)
		VALUES(?,?,?,0,?,?,?,?)`,
		owner, collateral, borrow, liqPrice, CDPStatusOpen, now, now)
	if err != nil {
		return 0, fmt.Errorf("cdp: 開倉寫入失敗: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetCDP 依 ID 回倉位。
func (s *Store) GetCDP(id int64) (*CDP, error) {
	row := s.db.QueryRow(`SELECT id, owner, collateral, debt, fee_accrued, liq_price, status, opened_at, updated_at
		FROM defi_cdps WHERE id=?`, id)
	c := &CDP{}
	if err := row.Scan(&c.ID, &c.Owner, &c.Collateral, &c.Debt, &c.FeeAccrued, &c.LiqPrice, &c.Status, &c.OpenedAt, &c.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("cdp: 倉位不存在")
		}
		return nil, err
	}
	return c, nil
}

// MyCDPs 回使用者全部倉位。
func (s *Store) MyCDPs(owner string) ([]CDP, error) {
	rows, err := s.db.Query(`SELECT id, owner, collateral, debt, fee_accrued, liq_price, status, opened_at, updated_at
		FROM defi_cdps WHERE owner=? ORDER BY id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CDP
	for rows.Next() {
		var c CDP
		if err := rows.Scan(&c.ID, &c.Owner, &c.Collateral, &c.Debt, &c.FeeAccrued, &c.LiqPrice, &c.Status, &c.OpenedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AccrueStabilityFee 計提穩定費（年息 2%）：FeeAccrued += Debt*APR*(天數)。
// 每次操作前應先計提；純引擎不自動排程，由 RPC 層在讀取/操作時觸發。
func (s *Store) AccrueStabilityFee(id int64, nowSec int64) error {
	c, err := s.GetCDP(id)
	if err != nil {
		return err
	}
	if c.Status != CDPStatusOpen || c.Debt <= 0 {
		return nil
	}
	days := float64(nowSec-c.UpdatedAt) / 86400.0
	if days <= 0 {
		return nil
	}
	fee := c.Debt * CDPStabilityAPR * days / 365.0 // 年息：days 為天數，換算年數
	if fee <= 0 {
		return nil
	}
	fee = math.Round(fee*1e6) / 1e6 // 收斂到 1e-6 顯示精度
	c.FeeAccrued += fee
	c.Debt += fee
	c.UpdatedAt = nowSec
	_, err = s.db.Exec(`UPDATE defi_cdps SET debt=?, fee_accrued=?, updated_at=? WHERE id=?`,
		c.Debt, c.FeeAccrued, nowSec, id)
	if err != nil {
		return fmt.Errorf("cdp: 計提穩定費失敗: %w", err)
	}
	return nil
}

// AddCollateral 追加抵押品。
func (s *Store) AddCollateral(id int64, amount float64) error {
	if amount <= 0 {
		return errors.New("cdp: 追加金額必須大於 0")
	}
	c, err := s.GetCDP(id)
	if err != nil {
		return err
	}
	if c.Status != CDPStatusOpen {
		return fmt.Errorf("cdp: 倉位狀態為 %s，不可追加", c.Status)
	}
	now := time.Now().Unix()
	if err := s.AccrueStabilityFee(id, now); err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE defi_cdps SET collateral=collateral+?, liq_price=?, updated_at=? WHERE id=? AND status=?`,
		amount, CDPLiqRatio*c.Debt/(c.Collateral+amount), now, id, CDPStatusOpen)
	if err != nil {
		return fmt.Errorf("cdp: 追加抵押失敗: %w", err)
	}
	return nil
}

// WithdrawCollateral 取回抵押品（取回後抵押率仍 ≥ 150%）。
func (s *Store) WithdrawCollateral(id int64, amount, priceUSD float64) error {
	if amount <= 0 {
		return errors.New("cdp: 取回金額必須大於 0")
	}
	if priceUSD <= 0 {
		return errors.New("cdp: TACm 參考價未配置")
	}
	c, err := s.GetCDP(id)
	if err != nil {
		return err
	}
	if c.Status != CDPStatusOpen {
		return fmt.Errorf("cdp: 倉位狀態為 %s，不可取回", c.Status)
	}
	if amount >= c.Collateral {
		return errors.New("cdp: 取回金額須小於抵押品（保留最低抵押）")
	}
	now := time.Now().Unix()
	if err := s.AccrueStabilityFee(id, now); err != nil {
		return err
	}
	c, _ = s.GetCDP(id)
	remain := c.Collateral - amount
	ratio := (remain * priceUSD) / c.Debt
	if ratio < CDPMinRatio-1e-9 {
		return fmt.Errorf("cdp: 取回後抵押率 %.2f 低於最低要求 %.2f", ratio, CDPMinRatio)
	}
	_, err = s.db.Exec(`UPDATE defi_cdps SET collateral=?, liq_price=?, updated_at=? WHERE id=? AND status=?`,
		remain, CDPLiqRatio*c.Debt/remain, now, id, CDPStatusOpen)
	if err != nil {
		return fmt.Errorf("cdp: 取回抵押失敗: %w", err)
	}
	return nil
}

// Repay 償還 TiUSD（先抵穩定費再抵本金）；remain=0 且無費時自動平倉。
// 回傳 (剩餘債務, 本次償還總額, error)。
func (s *Store) Repay(id int64, amountTiUSD float64) (float64, float64, error) {
	if amountTiUSD <= 0 {
		return 0, 0, errors.New("cdp: 償還金額必須大於 0")
	}
	c, err := s.GetCDP(id)
	if err != nil {
		return 0, 0, err
	}
	if c.Status != CDPStatusOpen {
		return 0, 0, fmt.Errorf("cdp: 倉位狀態為 %s，不可償還", c.Status)
	}
	now := time.Now().Unix()
	if err := s.AccrueStabilityFee(id, now); err != nil {
		return 0, 0, err
	}
	c, _ = s.GetCDP(id)
	if amountTiUSD > c.Debt {
		amountTiUSD = c.Debt // 償還不超過債務
	}
	remain := c.Debt - amountTiUSD
	status := CDPStatusOpen
	if remain <= 1e-9 {
		remain = 0
		status = CDPStatusClosed
	}
	_, err = s.db.Exec(`UPDATE defi_cdps SET debt=?, fee_accrued=?, status=?, updated_at=? WHERE id=?`,
		remain, c.FeeAccrued, status, now, id)
	if err != nil {
		return 0, 0, fmt.Errorf("cdp: 償還失敗: %w", err)
	}
	return remain, amountTiUSD, nil
}

// CDPRatio 回目前抵押率（依傳入價格）。
func (s *Store) CDPRatio(id int64, priceUSD float64) (float64, error) {
	c, err := s.GetCDP(id)
	if err != nil {
		return 0, err
	}
	if c.Status != CDPStatusOpen || c.Debt <= 0 {
		return 0, nil
	}
	return c.Collateral * priceUSD / c.Debt, nil
}

// Liquidatable 檢查倉位是否可清算（抵押率 < 清算線 130%）。
func (s *Store) Liquidatable(id int64, priceUSD float64) (bool, error) {
	if priceUSD <= 0 {
		return false, errors.New("cdp: TACm 參考價未配置")
	}
	c, err := s.GetCDP(id)
	if err != nil {
		return false, err
	}
	if c.Status != CDPStatusOpen || c.Debt <= 0 {
		return false, nil
	}
	ratio := c.Collateral * priceUSD / c.Debt
	return ratio < CDPLiqRatio-1e-9, nil
}

// Liquidate 清算倉位（DAI 式）：清算人償還全部債務 TiUSD，
// 可取得 債務×清算價格折算的抵押品×(1+罰金)；超過倉位抵押品時取全部抵押品。
// 回傳 (清算人可取得抵押品量, 需償還 TiUSD 量, error)。Store 標記 liquidated。
func (s *Store) Liquidate(id int64, priceUSD float64) (float64, float64, error) {
	if priceUSD <= 0 {
		return 0, 0, errors.New("cdp: TACm 參考價未配置")
	}
	liq, err := s.Liquidatable(id, priceUSD)
	if err != nil {
		return 0, 0, err
	}
	if !liq {
		return 0, 0, errors.New("cdp: 倉位抵押率未跌破清算線，不可清算")
	}
	c, _ := s.GetCDP(id)
	if err := s.AccrueStabilityFee(id, time.Now().Unix()); err != nil {
		return 0, 0, err
	}
	c, _ = s.GetCDP(id)
	debt := c.Debt
	seizable := debt * (1 + CDPLiqPenalty) / priceUSD
	if seizable > c.Collateral {
		seizable = c.Collateral
	}
	_, err = s.db.Exec(`UPDATE defi_cdps SET status=?, updated_at=? WHERE id=?`,
		CDPStatusLiq, time.Now().Unix(), id)
	if err != nil {
		return 0, 0, fmt.Errorf("cdp: 清算標記失敗: %w", err)
	}
	return seizable, debt, nil
}

// CDPMarketView 回 CDP 市場總覽（依傳入價格）。
func (s *Store) CDPMarketView(priceUSD float64) (*CDPMarket, error) {
	m := &CDPMarket{
		PriceUSD:        priceUSD,
		MinRatio:        CDPMinRatio,
		LiqRatio:        CDPLiqRatio,
		LiqPenalty:      CDPLiqPenalty,
		StabilityAPR:    CDPStabilityAPR,
		PriceConfigured: priceUSD > 0,
	}
	rows, err := s.db.Query(`SELECT id, collateral, debt FROM defi_cdps WHERE status=?`, CDPStatusOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var coll, debt float64
		if err := rows.Scan(&id, &coll, &debt); err != nil {
			return nil, err
		}
		m.TotalCollateral += coll
		m.TotalDebt += debt
		m.OpenPositions++
		if priceUSD > 0 && debt > 0 {
			ratio := coll * priceUSD / debt
			m.AvgRatio += ratio
			if ratio < CDPLiqRatio-1e-9 {
				m.Liquidatable++
			}
		}
	}
	if m.OpenPositions > 0 {
		m.AvgRatio = math.Round(m.AvgRatio/float64(m.OpenPositions)*100) / 100
	}
	return m, rows.Err()
}
