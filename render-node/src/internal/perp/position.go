package perp

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// 預設參數。
const (
	// FundingIntervalSec 資金費率計算的基礎時長（每小時結算一次）。
	FundingIntervalSec = 3600
	// MaxFundingRatePerHour 每小時資金費率上限（小數）。
	MaxFundingRatePerHour = 0.001 // 0.1% / hr
	// MinFundingRatePerHour 每小時資金費率下限。
	MinFundingRatePerHour = -0.001
	// LiquidationBonusBps 強平獎勵（bps，從被強平倉位的剩餘 margin 中提撥給清算人）。
	LiquidationBonusBps = 500 // 5%
)

// Deposit 入金（僅記 perp 帳本；實際錢包轉帳由 node 層做）。
func (s *Store) Deposit(trader, asset string, amount float64) error {
	trader = strings.TrimSpace(trader)
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if trader == "" || amount <= 0 || math.IsNaN(amount) {
		return fmt.Errorf("perp: 入金參數不合法")
	}
	if asset != "TACM" && asset != "TIUSD" && asset != "USDT" {
		return fmt.Errorf("perp: 不支援的資產: %s", asset)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.depositLocked(trader, asset, amount)
}

func (s *Store) depositLocked(trader, asset string, amount float64) error {
	_, err := s.db.Exec(`INSERT INTO perp_accounts(trader, asset, balance) VALUES(?,?,?)
		ON CONFLICT(trader, asset) DO UPDATE SET balance = balance + excluded.balance`,
		trader, asset, amount)
	if err != nil {
		return fmt.Errorf("perp: 入金寫庫: %w", err)
	}
	return nil
}

// Withdraw 出金（僅記 perp 帳本；實際錢包 InternalTransfer 由 node 層做）。
// 會校驗可用餘額足夠且不觸發強平（即出金後帳戶 equity 仍 >= 所有倉位維持保證金合計）。
func (s *Store) Withdraw(trader, asset string, amount float64) error {
	trader = strings.TrimSpace(trader)
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if trader == "" || amount <= 0 || math.IsNaN(amount) {
		return fmt.Errorf("perp: 出金參數不合法")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleAllLocked(time.Now().Unix()); err != nil {
		return err
	}
	free, err := s.freeBalanceLocked(trader, asset)
	if err != nil {
		return err
	}
	if free < amount {
		return fmt.Errorf("perp: 可用保證金不足（%.4f < %.4f）", free, amount)
	}
	// 校驗：出金後剩餘 free 加上所有未實現 PnL 仍 >= 維持保證金。
	equity, maintTotal, err := s.equityLocked(trader)
	if err != nil {
		return err
	}
	// equity 已含 free；扣除 amount 後：
	if equity-amount < maintTotal {
		return fmt.Errorf("perp: 出金後將低於維持保證金（equity %.4f - %.4f < %.4f）", equity, amount, maintTotal)
	}
	_, err = s.db.Exec(`UPDATE perp_accounts SET balance = balance - ? WHERE trader=? AND asset=?`, amount, trader, asset)
	if err != nil {
		return fmt.Errorf("perp: 出金寫庫: %w", err)
	}
	return nil
}

// OpenPosition 開倉。
// trader: 0x/trader 地址；symbol: BTC/ETH；side: long/short；leverage: 1~max；
// margin: 保證金數量（以 marginAsset 計）；marginAsset: TACM/TIUSD。
// 回傳建立的持倉、實際名目（notional，以 USD 計）、手續費（簡化為 0，未扣除）。
func (s *Store) OpenPosition(trader, symbol, side string, leverage float64, margin float64, marginAsset string) (*Position, float64, error) {
	trader = strings.TrimSpace(trader)
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	side = strings.ToLower(strings.TrimSpace(side))
	marginAsset = strings.ToUpper(strings.TrimSpace(marginAsset))
	if trader == "" {
		return nil, 0, fmt.Errorf("perp: trader 為空")
	}
	if side != "long" && side != "short" {
		return nil, 0, fmt.Errorf("perp: side 必須為 long/short")
	}
	if leverage <= 0 || math.IsNaN(leverage) {
		return nil, 0, fmt.Errorf("perp: leverage 不合法")
	}
	if margin <= 0 || math.IsNaN(margin) {
		return nil, 0, fmt.Errorf("perp: margin 必須 > 0")
	}
	if marginAsset != "TACM" && marginAsset != "TIUSD" && marginAsset != "USDT" {
		return nil, 0, fmt.Errorf("perp: 不支援的保證金資產: %s", marginAsset)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	if err := s.settleAllLocked(now); err != nil {
		return nil, 0, err
	}
	mkt, err := s.loadMarketLocked(symbol)
	if err != nil {
		return nil, 0, err
	}
	if leverage > float64(mkt.MaxLeverage) {
		return nil, 0, fmt.Errorf("perp: 槓桿 %.1fx 超過市場上限 %dx", leverage, mkt.MaxLeverage)
	}
	// 標記價格。
	mark, ok := s.priceLocked(symbol)
	if !ok || mark <= 0 {
		return nil, 0, fmt.Errorf("perp: 無 %s 標記價格", symbol)
	}
	// 校驗保證金餘額。
	free, err := s.freeBalanceLocked(trader, marginAsset)
	if err != nil {
		return nil, 0, err
	}
	if free < margin {
		return nil, 0, fmt.Errorf("perp: 可用保證金不足（%.4f < %.4f %s）", free, margin, marginAsset)
	}
	// 名目（以 USD 計）= margin * leverage * (marginAsset 在 USD 的簡化匯率=1)。
	// 為垂直切片簡化：將 margin 視為 USD 等值（TIUSD 正確；TACm 為近似）。
	notional := margin * leverage
	size := notional / mark // 基礎幣數量。
	if size <= 0 {
		return nil, 0, fmt.Errorf("perp: 計算 size 異常")
	}
	// 鎖定保證金：從 free 扣除（實際上 position.margin 記錄，帳戶表只記 free）。
	_, err = s.db.Exec(`UPDATE perp_accounts SET balance = balance - ? WHERE trader=? AND asset=?`, margin, trader, marginAsset)
	if err != nil {
		return nil, 0, fmt.Errorf("perp: 鎖定保證金: %w", err)
	}
	res, err := s.db.Exec(`INSERT INTO perp_positions(trader, symbol, side, size, entry_price, margin, margin_asset, leverage, last_funding_ts, open, opened_at)
		VALUES(?,?,?,?,?,?,?,?,?,1,?)`,
		trader, symbol, side, size, mark, margin, marginAsset, leverage, now, now)
	if err != nil {
		// 退回保證金。
		_, _ = s.db.Exec(`UPDATE perp_accounts SET balance = balance + ? WHERE trader=? AND asset=?`, margin, trader, marginAsset)
		return nil, 0, fmt.Errorf("perp: 寫入持倉: %w", err)
	}
	pid, _ := res.LastInsertId()
	// 更新 OI。
	if side == "long" {
		_, _ = s.db.Exec(`UPDATE perp_markets SET oi_long = oi_long + ? WHERE symbol=?`, size, symbol)
	} else {
		_, _ = s.db.Exec(`UPDATE perp_markets SET oi_short = oi_short + ? WHERE symbol=?`, size, symbol)
	}
	p := &Position{
		ID: pid, Trader: trader, Symbol: symbol, Side: side, Size: size,
		EntryPrice: mark, Margin: margin, MarginAsset: marginAsset,
		Leverage: leverage, LastFundingTs: now, Open: true, OpenedAt: now,
		MarkPrice: mark,
	}
	s.enrichPositionLocked(p)
	return p, notional, nil
}

// ClosePosition 平倉，回傳 realizedPnL（以 position.MarginAsset 計）與最終返還金額。
func (s *Store) ClosePosition(positionID int64) (realizedPnL float64, returned float64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	if err = s.settleAllLocked(now); err != nil {
		return 0, 0, err
	}
	p, err := s.loadPositionLocked(positionID)
	if err != nil {
		return 0, 0, err
	}
	if !p.Open {
		return 0, 0, fmt.Errorf("perp: 持倉已平倉")
	}
	mark, ok := s.priceLocked(p.Symbol)
	if !ok || mark <= 0 {
		return 0, 0, fmt.Errorf("perp: 無 %s 標記價格", p.Symbol)
	}
	// 未實現 PnL（USD 計，近似以 margin asset 計）。
	pnl := unrealizedPnL(p.Side, p.Size, p.EntryPrice, mark)
	// 退還：剩餘 margin + pnl（可為負，從帳戶 free 扣）。
	returned = p.Margin + pnl
	// 標記倉位已平。
	_, _ = s.db.Exec(`UPDATE perp_positions SET open=0 WHERE id=?`, positionID)
	// OI 減回。
	if p.Side == "long" {
		_, _ = s.db.Exec(`UPDATE perp_markets SET oi_long = MAX(0, oi_long - ?) WHERE symbol=?`, p.Size, p.Symbol)
	} else {
		_, _ = s.db.Exec(`UPDATE perp_markets SET oi_short = MAX(0, oi_short - ?) WHERE symbol=?`, p.Size, p.Symbol)
	}
	// 將 returned 加回 trader 的 margin asset free。
	if returned > 0 {
		if err := s.depositLocked(p.Trader, p.MarginAsset, returned); err != nil {
			return 0, 0, err
		}
	} else if returned < 0 {
		// 虧損超過 margin：從 free 扣。
		free, ferr := s.freeBalanceLocked(p.Trader, p.MarginAsset)
		if ferr != nil {
			return 0, 0, ferr
		}
		need := -returned
		if free < need {
			// 保證金不足（帳戶穿倉）：這裡記錄為負債（垂直切片簡化為允許負餘額）。
		}
		_, _ = s.db.Exec(`INSERT INTO perp_accounts(trader, asset, balance) VALUES(?,?,0)
			ON CONFLICT(trader, asset) DO UPDATE SET balance = balance - ?`, p.Trader, p.MarginAsset, need)
	}
	return pnl, returned, nil
}

// LiquidatePosition 強平倉位（回傳清算結果：清算人獎勵、剩餘歸還帳戶、PnL）。
// liquidator: 清算人地址（可為 "system"）。
func (s *Store) LiquidatePosition(positionID int64, liquidator string) (liqBonus float64, returned float64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	if err = s.settleAllLocked(now); err != nil {
		return 0, 0, err
	}
	p, err := s.loadPositionLocked(positionID)
	if err != nil {
		return 0, 0, err
	}
	if !p.Open {
		return 0, 0, fmt.Errorf("perp: 持倉已平倉")
	}
	mark, ok := s.priceLocked(p.Symbol)
	if !ok || mark <= 0 {
		return 0, 0, fmt.Errorf("perp: 無 %s 標記價格", p.Symbol)
	}
	pnl := unrealizedPnL(p.Side, p.Size, p.EntryPrice, mark)
	mkt, _ := s.loadMarketLocked(p.Symbol)
	maint := maintenanceMargin(p.Size, mark, mkt.MaintMarginBps)
	// 強平條件：margin + pnl <= maintMargin。
	equity := p.Margin + pnl
	if equity > maint {
		return 0, 0, fmt.Errorf("perp: 未達強平線（equity %.4f > maint %.4f）", equity, maint)
	}
	// 標記平倉。
	_, _ = s.db.Exec(`UPDATE perp_positions SET open=0 WHERE id=?`, positionID)
	if p.Side == "long" {
		_, _ = s.db.Exec(`UPDATE perp_markets SET oi_long = MAX(0, oi_long - ?) WHERE symbol=?`, p.Size, p.Symbol)
	} else {
		_, _ = s.db.Exec(`UPDATE perp_markets SET oi_short = MAX(0, oi_short - ?) WHERE symbol=?`, p.Size, p.Symbol)
	}
	// 清算獎勵：從 equity 中提撥（若 equity <= 0，則獎勵為 0）。
	if equity > 0 {
		liqBonus = equity * float64(LiquidationBonusBps) / 10000
		returned = equity - liqBonus
		// 剩餘歸還 trader。
		_ = s.depositLocked(p.Trader, p.MarginAsset, returned)
		// 獎勵給清算人（若指定）。
		if liquidator != "" && liquidator != "system" {
			_ = s.depositLocked(liquidator, p.MarginAsset, liqBonus)
		}
	} else {
		// 穿倉：trader 帳戶虧空（垂直切片簡化：記負債）。
		_, _ = s.db.Exec(`INSERT INTO perp_accounts(trader, asset, balance) VALUES(?,?,0)
			ON CONFLICT(trader, asset) DO UPDATE SET balance = balance + ?`, p.Trader, p.MarginAsset, equity)
	}
	return liqBonus, returned, nil
}

// AddMargin 對持倉追加保證金（降低強平風險）。
func (s *Store) AddMargin(positionID int64, amount float64) error {
	if amount <= 0 || math.IsNaN(amount) {
		return fmt.Errorf("perp: 追加保證金必須 > 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	if err := s.settleAllLocked(now); err != nil {
		return err
	}
	p, err := s.loadPositionLocked(positionID)
	if err != nil {
		return err
	}
	if !p.Open {
		return fmt.Errorf("perp: 持倉已平倉")
	}
	free, err := s.freeBalanceLocked(p.Trader, p.MarginAsset)
	if err != nil {
		return err
	}
	if free < amount {
		return fmt.Errorf("perp: 可用保證金不足（%.4f < %.4f）", free, amount)
	}
	_, _ = s.db.Exec(`UPDATE perp_accounts SET balance = balance - ? WHERE trader=? AND asset=?`, amount, p.Trader, p.MarginAsset)
	_, _ = s.db.Exec(`UPDATE perp_positions SET margin = margin + ? WHERE id=?`, amount, positionID)
	return nil
}

// RemoveMargin 從持倉減回保證金（需校驗減後仍高於維持保證金）。
func (s *Store) RemoveMargin(positionID int64, amount float64) error {
	if amount <= 0 || math.IsNaN(amount) {
		return fmt.Errorf("perp: 減少保證金必須 > 0")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	if err := s.settleAllLocked(now); err != nil {
		return err
	}
	p, err := s.loadPositionLocked(positionID)
	if err != nil {
		return err
	}
	if !p.Open {
		return fmt.Errorf("perp: 持倉已平倉")
	}
	if amount > p.Margin {
		return fmt.Errorf("perp: 減少金額超過倉位保證金（%.4f > %.4f）", amount, p.Margin)
	}
	mark, ok := s.priceLocked(p.Symbol)
	if !ok || mark <= 0 {
		return fmt.Errorf("perp: 無 %s 標記價格", p.Symbol)
	}
	mkt, _ := s.loadMarketLocked(p.Symbol)
	pnl := unrealizedPnL(p.Side, p.Size, p.EntryPrice, mark)
	newEquity := (p.Margin - amount) + pnl
	maint := maintenanceMargin(p.Size, mark, mkt.MaintMarginBps)
	if newEquity < maint {
		return fmt.Errorf("perp: 減少 %.4f 後 equity=%.4f < maint=%.4f（拒絕）", amount, newEquity, maint)
	}
	_, _ = s.db.Exec(`UPDATE perp_positions SET margin = margin - ? WHERE id=?`, amount, positionID)
	_ = s.depositLocked(p.Trader, p.MarginAsset, amount)
	return nil
}

// SettleFunding 顯式觸發某市場（或全部市場，symbol=""）的資金費率結算。
func (s *Store) SettleFunding(now int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleAllLocked(now)
}

// settleAllLocked 惰性結算所有市場的 funding（呼叫者需持有 s.mu）。
func (s *Store) settleAllLocked(now int64) error {
	markets, err := s.marketsLocked()
	if err != nil {
		return err
	}
	for _, m := range markets {
		if m.LastFundingTs <= 0 {
			_, _ = s.db.Exec(`UPDATE perp_markets SET last_funding_ts=? WHERE symbol=?`, now, m.Symbol)
			continue
		}
		elapsed := now - m.LastFundingTs
		if elapsed <= 0 {
			continue
		}
		// 計算當前 funding rate（依 OI 失衡）。
		rate := fundingRateFromOI(m.OpenInterestLong, m.OpenInterestShort)
		// 先讀出所有 open 倉位（避免單連線下 rows 開著又 Exec 造成死鎖）。
		type posDelta struct {
			id     int64
			side   string
			size   float64
			margin float64
		}
		var posList []posDelta
		{
			rows, err := s.db.Query(`SELECT id, side, size, margin FROM perp_positions WHERE symbol=? AND open=1`, m.Symbol)
			if err != nil {
				return fmt.Errorf("perp: 結算 %s 倉位查詢: %w", m.Symbol, err)
			}
			for rows.Next() {
				var pd posDelta
				if err := rows.Scan(&pd.id, &pd.side, &pd.size, &pd.margin); err != nil {
					rows.Close()
					return fmt.Errorf("perp: 掃描倉位: %w", err)
				}
				posList = append(posList, pd)
			}
			rows.Close()
		}
		mark := m.MarkPrice
		if mark <= 0 {
			if p, ok := s.priceLocked(m.Symbol); ok {
				mark = p
			}
		}
		hours := float64(elapsed) / float64(FundingIntervalSec)
		for _, pd := range posList {
			if mark <= 0 {
				continue
			}
			funding := pd.size * mark * rate * hours
			switch pd.side {
			case "long":
				_, _ = s.db.Exec(`UPDATE perp_positions SET margin = margin - ?, last_funding_ts=? WHERE id=?`, funding, now, pd.id)
			case "short":
				_, _ = s.db.Exec(`UPDATE perp_positions SET margin = margin + ?, last_funding_ts=? WHERE id=?`, funding, now, pd.id)
			}
		}
		// 推進市場 lastFundingTs，並更新 funding_rate。
		_, _ = s.db.Exec(`UPDATE perp_markets SET last_funding_ts=?, funding_rate=? WHERE symbol=?`, now, rate, m.Symbol)
	}
	return nil
}

// fundingRateFromOI 依 OI 失衡計算每小時 funding rate（ clamped）。
func fundingRateFromOI(oiLong, oiShort float64) float64 {
	total := oiLong + oiShort
	if total <= 0 {
		return 0
	}
	// 失衡比例：long 偏多 → 多頭付空頭 → rate > 0（long.margin -= rate）。
	imbalance := (oiLong - oiShort) / total // [-1, 1]
	rate := imbalance * 0.0001                // 最大 ±0.01%/hr 基礎，再 clamp。
	if rate > MaxFundingRatePerHour {
		rate = MaxFundingRatePerHour
	}
	if rate < MinFundingRatePerHour {
		rate = MinFundingRatePerHour
	}
	return rate
}

// unrealizedPnL 計算未實現 PnL（USD 計）。
func unrealizedPnL(side string, size, entry, mark float64) float64 {
	if side == "long" {
		return (mark - entry) * size
	}
	return (entry - mark) * size
}

// maintenanceMargin 計算維持保證金（USD 計）。
func maintenanceMargin(size, mark float64, maintBps int64) float64 {
	return size * mark * float64(maintBps) / 10000
}

// liqPrice 估算強平價。
// 強平條件：margin + unrealizedPnL <= size*mark*maintBps/10000
//
//	long : mark <= (entry*size - margin) / (size*(1-maintF)) = (entry - margin/size) / (1-maintF)
//	short: mark >= (entry*size + margin) / (size*(1+maintF)) = (entry + margin/size) / (1+maintF)
func liqPrice(side string, size, entry, margin float64, maintBps int64) float64 {
	maintF := float64(maintBps) / 10000
	if size <= 0 {
		return 0
	}
	marginPerUnit := margin / size
	if side == "long" {
		return (entry - marginPerUnit) / (1 - maintF)
	}
	return (entry + marginPerUnit) / (1 + maintF)
}

// freeBalanceLocked 取 trader 某資產可用餘額。
func (s *Store) freeBalanceLocked(trader, asset string) (float64, error) {
	var bal float64
	err := s.db.QueryRow(`SELECT balance FROM perp_accounts WHERE trader=? AND asset=?`, trader, asset).Scan(&bal)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("perp: 查詢餘額: %w", err)
	}
	return bal, nil
}

// loadPositionLocked 讀持倉。
func (s *Store) loadPositionLocked(id int64) (*Position, error) {
	var p Position
	var open int
	err := s.db.QueryRow(`SELECT id, trader, symbol, side, size, entry_price, margin, margin_asset, leverage, last_funding_ts, open, opened_at FROM perp_positions WHERE id=?`, id).
		Scan(&p.ID, &p.Trader, &p.Symbol, &p.Side, &p.Size, &p.EntryPrice, &p.Margin, &p.MarginAsset, &p.Leverage, &p.LastFundingTs, &open, &p.OpenedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("perp: 持倉不存在: %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("perp: 讀取持倉: %w", err)
	}
	p.Open = open == 1
	s.enrichPositionLocked(&p)
	return &p, nil
}

// enrichPositionLocked 補上動態欄位（mark / pnl / maint / liqPrice）。
func (s *Store) enrichPositionLocked(p *Position) {
	if !p.Open {
		return
	}
	if mark, ok := s.priceLocked(p.Symbol); ok {
		p.MarkPrice = mark
		p.UnrealizedPnL = unrealizedPnL(p.Side, p.Size, p.EntryPrice, mark)
		mkt, err := s.loadMarketLocked(p.Symbol)
		if err == nil {
			p.MaintMargin = maintenanceMargin(p.Size, mark, mkt.MaintMarginBps)
			p.LiqPrice = liqPrice(p.Side, p.Size, p.EntryPrice, p.Margin, mkt.MaintMarginBps)
		}
	}
}

// GetPosition 查單一持倉。
func (s *Store) GetPosition(id int64) (*Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleAllLocked(time.Now().Unix()); err != nil {
		return nil, err
	}
	return s.loadPositionLocked(id)
}

// ListPositions 列出某 trader 的持倉（openOnly=true 只列未平倉）。
func (s *Store) ListPositions(trader string, openOnly bool) ([]Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleAllLocked(time.Now().Unix()); err != nil {
		return nil, err
	}
	q := `SELECT id, trader, symbol, side, size, entry_price, margin, margin_asset, leverage, last_funding_ts, open, opened_at FROM perp_positions WHERE trader=?`
	if openOnly {
		q += ` AND open=1`
	}
	q += ` ORDER BY opened_at DESC LIMIT 200`
	rows, err := s.db.Query(q, trader)
	if err != nil {
		return nil, fmt.Errorf("perp: 列出持倉: %w", err)
	}
	type rawPos struct {
		p Position
	}
	var raw []rawPos
	for rows.Next() {
		var p Position
		var open int
		if err := rows.Scan(&p.ID, &p.Trader, &p.Symbol, &p.Side, &p.Size, &p.EntryPrice, &p.Margin, &p.MarginAsset, &p.Leverage, &p.LastFundingTs, &open, &p.OpenedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("perp: 掃描持倉: %w", err)
		}
		p.Open = open == 1
		raw = append(raw, rawPos{p})
	}
	rows.Close()
	out := make([]Position, 0, len(raw))
	for _, r := range raw {
		s.enrichPositionLocked(&r.p)
		out = append(out, r.p)
	}
	return out, nil
}

// Markets 列出市場（內部先 SettleFunding）。
func (s *Store) Markets() ([]Market, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleAllLocked(time.Now().Unix()); err != nil {
		return nil, err
	}
	return s.marketsLocked()
}

// GetMarket 查單一市場（內部先 SettleFunding）。
func (s *Store) GetMarket(symbol string) (Market, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleAllLocked(time.Now().Unix()); err != nil {
		return Market{}, err
	}
	return s.loadMarketLocked(symbol)
}

// GetAccount 取 trader 帳戶（含 equity、totalMargin 動態欄位）。
func (s *Store) GetAccount(trader string) (*Account, error) {
	trader = strings.TrimSpace(trader)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleAllLocked(time.Now().Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT asset, balance FROM perp_accounts WHERE trader=?`, trader)
	if err != nil {
		return nil, fmt.Errorf("perp: 查詢帳戶: %w", err)
	}
	defer rows.Close()
	a := &Account{Trader: trader, Balances: map[string]float64{}}
	for rows.Next() {
		var asset string
		var bal float64
		if err := rows.Scan(&asset, &bal); err != nil {
			return nil, fmt.Errorf("perp: 掃描帳戶: %w", err)
		}
		a.Balances[asset] = bal
	}
	eq, maintTotal, err := s.equityLocked(trader)
	if err != nil {
		return nil, err
	}
	a.Equity = eq
	a.TotalMargin = maintTotal
	return a, nil
}

// equityLocked 計算帳戶 equity（free 總和 + 所有 open 倉位的 unrealizedPnL）。
// 回傳：equity、所有 open 倉位維持保證金合計（USD 近似）。
func (s *Store) equityLocked(trader string) (float64, float64, error) {
	freeSum := 0.0
	for _, asset := range []string{"TACM", "TIUSD", "USDT"} {
		b, err := s.freeBalanceLocked(trader, asset)
		if err != nil {
			return 0, 0, err
		}
		freeSum += b
	}
	var ids []int64
	{
		rows, err := s.db.Query(`SELECT id FROM perp_positions WHERE trader=? AND open=1`, trader)
		if err != nil {
			return 0, 0, fmt.Errorf("perp: 列 open 倉位: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, 0, err
			}
			ids = append(ids, id)
		}
		rows.Close()
	}
	pnlSum := 0.0
	maintTotal := 0.0
	for _, id := range ids {
		p, err := s.loadPositionLocked(id)
		if err != nil {
			continue
		}
		pnlSum += p.UnrealizedPnL
		maintTotal += p.MaintMargin
	}
	return freeSum + pnlSum, maintTotal, nil
}
