package dexv3

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// ===== tick ↔ √price 換算 =====

// TickToSqrtPrice tick → √price（price = 1.0001^tick；√price = 1.0001^(tick/2)）。
func TickToSqrtPrice(tick int) float64 {
	return math.Pow(1.0001, float64(tick)/2.0)
}

// SqrtPriceToTick √price → tick（四捨五入至整數）。
func SqrtPriceToTick(s float64) int {
	return int(math.Round(2.0 * math.Log(s) / math.Log(1.0001)))
}

// PriceToSqrtPrice price → √price。
func PriceToSqrtPrice(price float64) float64 { return math.Sqrt(price) }

// ===== 流動性 ↔ 金額 =====
//
// 區間 [sqrtA, sqrtB]（sqrtA=√price(tickLower), sqrtB=√price(tickUpper)），當前 √price=S：
//
//	S ≤ sqrtA（價格在區間下方）：位置全為 token0
//	  amt0 = L·(sqrtB−sqrtA)/(sqrtA·sqrtB)，amt1 = 0
//	S ≥ sqrtB（價格在區間上方）：位置全為 token1
//	  amt1 = L·(sqrtB−sqrtA)，amt0 = 0
//	sqrtA < S < sqrtB（區間內）：
//	  amt0 = L·(sqrtB−S)/(S·sqrtB)，amt1 = L·(S−sqrtA)

// amountsForLiquidity 給定 L 回傳對應應投入/應退回的 token0/token1 數量。
func amountsForLiquidity(L, sqrtA, sqrtB, S float64) (amt0, amt1 float64) {
	if L <= 0 || sqrtA <= 0 || sqrtB <= 0 || sqrtA >= sqrtB {
		return 0, 0
	}
	if S <= sqrtA {
		return L * (sqrtB - sqrtA) / (sqrtA * sqrtB), 0
	}
	if S >= sqrtB {
		return 0, L * (sqrtB - sqrtA)
	}
	return L * (sqrtB - S) / (S * sqrtB), L * (S - sqrtA)
}

// liquidityForAmounts 給定期望投入的 amt0/amt1 回傳可支援的 L（區間內取兩者可支援的較小值）。
func liquidityForAmounts(amt0, amt1, sqrtA, sqrtB, S float64) float64 {
	if sqrtA <= 0 || sqrtB <= 0 || sqrtA >= sqrtB {
		return 0
	}
	if S <= sqrtA {
		if amt0 <= 0 {
			return 0
		}
		return amt0 * sqrtA * sqrtB / (sqrtB - sqrtA)
	}
	if S >= sqrtB {
		if amt1 <= 0 {
			return 0
		}
		return amt1 / (sqrtB - sqrtA)
	}
	L0, L1 := 0.0, 0.0
	if d := sqrtB - S; d > 1e-12 && amt0 > 0 {
		L0 = amt0 * S * sqrtB / d
	}
	if d := S - sqrtA; d > 1e-12 && amt1 > 0 {
		L1 = amt1 / d
	}
	switch {
	case L0 <= 0:
		return L1
	case L1 <= 0:
		return L0
	case L0 < L1:
		return L0
	default:
		return L1
	}
}

// ===== 內部交易輔助 =====

func loadPoolTx(tx *sql.Tx, poolId int64) (*Pool, error) {
	p, err := scanPool(tx.QueryRow(`SELECT `+poolCols+` FROM dexv3_pools WHERE id=?`, poolId))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dexv3: 池不存在（id=%d）", poolId)
	}
	if err != nil {
		return nil, fmt.Errorf("dexv3: 查詢池: %w", err)
	}
	return p, nil
}

func savePoolTx(tx *sql.Tx, p *Pool) error {
	_, err := tx.Exec(`UPDATE dexv3_pools SET sqrt_price=?, tick=?, liquidity=?, fee_growth_global0=?, fee_growth_global1=? WHERE id=?`,
		p.SqrtPrice, p.Tick, p.Liquidity, p.FeeGrowthGlobal0, p.FeeGrowthGlobal1, p.Id)
	if err != nil {
		return fmt.Errorf("dexv3: 更新池: %w", err)
	}
	return nil
}

// upsertTick 變動某 tick 的 liquidityNet（歸零則刪除）。
func upsertTick(tx *sql.Tx, poolId int64, tick int, delta float64) error {
	var cur float64
	err := tx.QueryRow(`SELECT liquidity_net FROM dexv3_ticks WHERE pool_id=? AND tick=?`, poolId, tick).Scan(&cur)
	if err == sql.ErrNoRows {
		if math.Abs(delta) < 1e-12 {
			return nil
		}
		if _, err := tx.Exec(`INSERT INTO dexv3_ticks(pool_id,tick,liquidity_net) VALUES(?,?,?)`, poolId, tick, delta); err != nil {
			return fmt.Errorf("dexv3: 寫入 tick: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("dexv3: 讀取 tick: %w", err)
	}
	newv := cur + delta
	if math.Abs(newv) < 1e-12 {
		if _, err := tx.Exec(`DELETE FROM dexv3_ticks WHERE pool_id=? AND tick=?`, poolId, tick); err != nil {
			return fmt.Errorf("dexv3: 清除 tick: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(`UPDATE dexv3_ticks SET liquidity_net=? WHERE pool_id=? AND tick=?`, newv, poolId, tick); err != nil {
		return fmt.Errorf("dexv3: 更新 tick: %w", err)
	}
	return nil
}

// accrueFees 惰性結算位置累積費用：若當前價格落在位置區間內，
// 依「活躍流動性佔比」把 feeGrowthGlobal 的增量折成 tokensOwedX。
func accrueFees(p *Pool, pos *Position) {
	if pos.Liquidity > 0 && pos.TickLower <= p.Tick && p.Tick < pos.TickUpper {
		pos.TokensOwed0 += pos.Liquidity * (p.FeeGrowthGlobal0 - pos.FeeGrowthInside0Last)
		pos.TokensOwed1 += pos.Liquidity * (p.FeeGrowthGlobal1 - pos.FeeGrowthInside1Last)
	}
	pos.FeeGrowthInside0Last = p.FeeGrowthGlobal0
	pos.FeeGrowthInside1Last = p.FeeGrowthGlobal1
}

func savePositionTx(tx *sql.Tx, pos *Position) error {
	_, err := tx.Exec(`UPDATE dexv3_positions SET liquidity=?, fee_growth_inside0_last=?, fee_growth_inside1_last=?, tokens_owed0=?, tokens_owed1=? WHERE id=?`,
		pos.Liquidity, pos.FeeGrowthInside0Last, pos.FeeGrowthInside1Last, pos.TokensOwed0, pos.TokensOwed1, pos.Id)
	if err != nil {
		return fmt.Errorf("dexv3: 更新位置: %w", err)
	}
	return nil
}

func loadPositionTx(tx *sql.Tx, positionId int64) (*Position, error) {
	pos, err := scanPosition(tx.QueryRow(`SELECT id,owner,pool_id,tick_lower,tick_upper,liquidity,fee_growth_inside0_last,fee_growth_inside1_last,tokens_owed0,tokens_owed1 FROM dexv3_positions WHERE id=?`, positionId))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dexv3: 位置不存在（id=%d）", positionId)
	}
	if err != nil {
		return nil, fmt.Errorf("dexv3: 查詢位置: %w", err)
	}
	return pos, nil
}

// ===== mint / increase / decrease / collect =====

// MintPosition 建立新位置（owner, pool, [tickLower,tickUpper]，期望投入 amt0/amt1）。
// 回傳 positionId、實際使用的 amount0/amount1、新增的 liquidity。
func (s *Store) MintPosition(owner string, poolId int64, tickLower, tickUpper int, amt0, amt1 float64) (*MintResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return nil, fmt.Errorf("dexv3: owner 不可為空")
	}
	if amt0 < 0 || amt1 < 0 {
		return nil, fmt.Errorf("dexv3: 投入量不可為負")
	}
	if amt0 == 0 && amt1 == 0 {
		return nil, fmt.Errorf("dexv3: 投入量不可全為 0")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dexv3: 開啟交易: %w", err)
	}
	defer tx.Rollback()

	p, err := loadPoolTx(tx, poolId)
	if err != nil {
		return nil, err
	}
	tickLower = roundTick(tickLower, p.TickSpacing)
	tickUpper = roundTick(tickUpper, p.TickSpacing)
	if tickLower >= tickUpper {
		return nil, fmt.Errorf("dexv3: tickLower 必須小於 tickUpper（已對齊 spacing=%d）", p.TickSpacing)
	}
	sqrtA := TickToSqrtPrice(tickLower)
	sqrtB := TickToSqrtPrice(tickUpper)

	L := liquidityForAmounts(amt0, amt1, sqrtA, sqrtB, p.SqrtPrice)
	if L <= 0 {
		return nil, fmt.Errorf("dexv3: 投入量不足以建立流動性（請在區間內提供適量 token）")
	}
	act0, act1 := amountsForLiquidity(L, sqrtA, sqrtB, p.SqrtPrice)

	now := time.Now().Unix()
	res, err := tx.Exec(`INSERT INTO dexv3_positions(owner,pool_id,tick_lower,tick_upper,liquidity,fee_growth_inside0_last,fee_growth_inside1_last,tokens_owed0,tokens_owed1,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		owner, poolId, tickLower, tickUpper, L, p.FeeGrowthGlobal0, p.FeeGrowthGlobal1, 0.0, 0.0, now)
	if err != nil {
		return nil, fmt.Errorf("dexv3: 建立位置: %w", err)
	}
	posId, _ := res.LastInsertId()

	if err := upsertTick(tx, poolId, tickLower, +L); err != nil {
		return nil, err
	}
	if err := upsertTick(tx, poolId, tickUpper, -L); err != nil {
		return nil, err
	}
	// 若新位置涵蓋當前價格，池活躍流動性直接增加 L。
	if tickLower <= p.Tick && p.Tick < tickUpper {
		p.Liquidity += L
		if err := savePoolTx(tx, p); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dexv3: 提交 mint: %w", err)
	}
	return &MintResult{PositionId: posId, Liquidity: L, Amount0: act0, Amount1: act1}, nil
}

// IncreasePosition 增加既有位置的流動性（指定額外投入 amt0/amt1）。
func (s *Store) IncreasePosition(positionId int64, amt0, amt1 float64) (*MintResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if amt0 < 0 || amt1 < 0 {
		return nil, fmt.Errorf("dexv3: 投入量不可為負")
	}
	if amt0 == 0 && amt1 == 0 {
		return nil, fmt.Errorf("dexv3: 投入量不可全為 0")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dexv3: 開啟交易: %w", err)
	}
	defer tx.Rollback()

	pos, err := loadPositionTx(tx, positionId)
	if err != nil {
		return nil, err
	}
	p, err := loadPoolTx(tx, pos.PoolId)
	if err != nil {
		return nil, err
	}
	// 先結算既有費用（避免增加流動性時把舊費用併入新 L 基準）。
	accrueFees(p, pos)

	sqrtA := TickToSqrtPrice(pos.TickLower)
	sqrtB := TickToSqrtPrice(pos.TickUpper)
	deltaL := liquidityForAmounts(amt0, amt1, sqrtA, sqrtB, p.SqrtPrice)
	if deltaL <= 0 {
		return nil, fmt.Errorf("dexv3: 投入量不足以增加流動性")
	}
	act0, act1 := amountsForLiquidity(deltaL, sqrtA, sqrtB, p.SqrtPrice)

	pos.Liquidity += deltaL
	if err := savePositionTx(tx, pos); err != nil {
		return nil, err
	}
	if err := upsertTick(tx, pos.PoolId, pos.TickLower, +deltaL); err != nil {
		return nil, err
	}
	if err := upsertTick(tx, pos.PoolId, pos.TickUpper, -deltaL); err != nil {
		return nil, err
	}
	if pos.TickLower <= p.Tick && p.Tick < pos.TickUpper {
		p.Liquidity += deltaL
		if err := savePoolTx(tx, p); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dexv3: 提交 increase: %w", err)
	}
	return &MintResult{PositionId: pos.Id, Liquidity: pos.Liquidity, Amount0: act0, Amount1: act1}, nil
}

// DecreasePosition 撤出部分流動性（removeL 為要減少的 L；<=0 或 >=全部時視為全撤）。
// 回傳應退回給使用者的 token0/token1 數量。
func (s *Store) DecreasePosition(positionId int64, removeL float64) (*DecreaseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if removeL <= 0 {
		return nil, fmt.Errorf("dexv3: 撤流量必須為正")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dexv3: 開啟交易: %w", err)
	}
	defer tx.Rollback()

	pos, err := loadPositionTx(tx, positionId)
	if err != nil {
		return nil, err
	}
	p, err := loadPoolTx(tx, pos.PoolId)
	if err != nil {
		return nil, err
	}
	accrueFees(p, pos)

	if removeL >= pos.Liquidity {
		removeL = pos.Liquidity
	}
	if removeL <= 0 {
		return nil, fmt.Errorf("dexv3: 位置已無流動性可撤")
	}
	sqrtA := TickToSqrtPrice(pos.TickLower)
	sqrtB := TickToSqrtPrice(pos.TickUpper)
	act0, act1 := amountsForLiquidity(removeL, sqrtA, sqrtB, p.SqrtPrice)

	pos.Liquidity -= removeL
	if err := savePositionTx(tx, pos); err != nil {
		return nil, err
	}
	if err := upsertTick(tx, pos.PoolId, pos.TickLower, -removeL); err != nil {
		return nil, err
	}
	if err := upsertTick(tx, pos.PoolId, pos.TickUpper, +removeL); err != nil {
		return nil, err
	}
	if pos.TickLower <= p.Tick && p.Tick < pos.TickUpper {
		p.Liquidity -= removeL
		if p.Liquidity < 0 {
			p.Liquidity = 0
		}
		if err := savePoolTx(tx, p); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dexv3: 提交 decrease: %w", err)
	}
	return &DecreaseResult{PositionId: pos.Id, LiquidityRemaining: pos.Liquidity, Amount0: act0, Amount1: act1}, nil
}

// CollectFees 領取位置累積的手續費，並將 tokensOwedX 歸零。
func (s *Store) CollectFees(positionId int64) (*CollectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dexv3: 開啟交易: %w", err)
	}
	defer tx.Rollback()

	pos, err := loadPositionTx(tx, positionId)
	if err != nil {
		return nil, err
	}
	p, err := loadPoolTx(tx, pos.PoolId)
	if err != nil {
		return nil, err
	}
	accrueFees(p, pos)
	owed0, owed1 := pos.TokensOwed0, pos.TokensOwed1
	if owed0 < 0 {
		owed0 = 0
	}
	if owed1 < 0 {
		owed1 = 0
	}
	pos.TokensOwed0 = 0
	pos.TokensOwed1 = 0
	if err := savePositionTx(tx, pos); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dexv3: 提交 collect: %w", err)
	}
	return &CollectResult{PositionId: pos.Id, Amount0: owed0, Amount1: owed1}, nil
}

// ===== swap =====

// Swap 執行集中流動性兌換：
//
//	zeroForOne=true  → 賣 token0（輸入）買 token1（輸出），價格下跌、S 遞減；
//	zeroForOne=false → 賣 token1（輸入）買 token0（輸出），價格上漲、S 遞增。
//
// amountIn 為毛額輸入；手續費 = amountIn × feeTier/10000 從輸入扣除。
// 跨多個已初始化 tick 分段吃單，回傳實際耗用輸入、輸出、費用、新 √price/tick/L。
func (s *Store) Swap(poolId int64, zeroForOne bool, amountIn float64) (*SwapResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if amountIn <= 0 {
		return nil, fmt.Errorf("dexv3: 輸入量必須為正")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("dexv3: 開啟交易: %w", err)
	}
	defer tx.Rollback()

	p, err := loadPoolTx(tx, poolId)
	if err != nil {
		return nil, err
	}
	if p.Liquidity <= 0 {
		return nil, fmt.Errorf("dexv3: 池目前無活躍流動性（請先 mint 涵蓋當前價格的位置）")
	}

	feeRate := float64(p.FeeTier) / 10000.0
	S := p.SqrtPrice
	tick := p.Tick
	L := p.Liquidity
	fg0, fg1 := p.FeeGrowthGlobal0, p.FeeGrowthGlobal1

	grossLeft := amountIn
	totalFee := 0.0
	totalOut := 0.0

	for grossLeft > 1e-12 {
		if L <= 0 {
			break
		}
		// 找出下一個已初始化的對手 tick（方向：zeroForOne 往下找，反之往上找）。
		var nextTick int
		var row *sql.Row
		if zeroForOne {
			row = tx.QueryRow(`SELECT tick FROM dexv3_ticks WHERE pool_id=? AND tick<? AND ABS(liquidity_net)>1e-12 ORDER BY tick DESC LIMIT 1`, poolId, tick)
		} else {
			row = tx.QueryRow(`SELECT tick FROM dexv3_ticks WHERE pool_id=? AND tick>? AND ABS(liquidity_net)>1e-12 ORDER BY tick ASC LIMIT 1`, poolId, tick)
		}
		if err := row.Scan(&nextTick); err != nil {
			if zeroForOne {
				nextTick = minTick
			} else {
				nextTick = maxTick
			}
		}
		Snext := TickToSqrtPrice(nextTick)

		if zeroForOne {
			if Snext >= S {
				break
			}
			// 本分段最多可吃的淨 token0：maxNet = L·(S−Snext)/(S·Snext)
			maxNet := L * (S - Snext) / (S * Snext)
			maxGross := maxNet / (1 - feeRate)
			grossStep := grossLeft
			if grossStep > maxGross {
				grossStep = maxGross
			}
			netStep := grossStep * (1 - feeRate)
			feeStep := grossStep - netStep
			// zeroForOne：token0 買 token1，S 遞減：1/S_new = 1/S + netStep/L
			Snew := 1.0 / (1.0/S + netStep/L)
			if Snew < Snext {
				Snew = Snext
			}
			outStep := L * (S - Snew) // token1 輸出
			totalOut += outStep
			totalFee += feeStep
			fg0 += feeStep / L
			grossLeft -= grossStep
			S = Snew
			// 穿越 nextTick 往下：活躍 L 減去該 tick 的 liquidityNet。
			if S <= Snext+1e-12 {
				var net float64
				if err := tx.QueryRow(`SELECT liquidity_net FROM dexv3_ticks WHERE pool_id=? AND tick=?`, poolId, nextTick).Scan(&net); err == nil {
					L -= net
				}
				tick = nextTick
			}
		} else {
			if Snext <= S {
				break
			}
			// 本分段最多可吃的淨 token1：maxNet = L·(Snext−S)
			maxNet := L * (Snext - S)
			maxGross := maxNet / (1 - feeRate)
			grossStep := grossLeft
			if grossStep > maxGross {
				grossStep = maxGross
			}
			netStep := grossStep * (1 - feeRate)
			feeStep := grossStep - netStep
			Snew := S + netStep/L
			if Snew > Snext {
				Snew = Snext
			}
			// oneForZero：token1 買 token0，S 遞增：token0 輸出 = L·(1/S − 1/Snew)
			outStep := L * (Snew - S) / (S * Snew)
			totalOut += outStep
			totalFee += feeStep
			fg1 += feeStep / L
			grossLeft -= grossStep
			S = Snew
			// 穿越 nextTick 往上：活躍 L 加上該 tick 的 liquidityNet。
			if S >= Snext-1e-12 {
				var net float64
				if err := tx.QueryRow(`SELECT liquidity_net FROM dexv3_ticks WHERE pool_id=? AND tick=?`, poolId, nextTick).Scan(&net); err == nil {
					L += net
				}
				tick = nextTick
			}
		}
	}

	newTick := SqrtPriceToTick(S)
	p.SqrtPrice = S
	p.Tick = newTick
	p.Liquidity = L
	p.FeeGrowthGlobal0 = fg0
	p.FeeGrowthGlobal1 = fg1
	if err := savePoolTx(tx, p); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dexv3: 提交 swap: %w", err)
	}
	return &SwapResult{
		PoolId: p.Id, ZeroForOne: zeroForOne,
		AmountIn: amountIn - grossLeft, AmountOut: totalOut, Fee: totalFee,
		SqrtPrice: S, Tick: newTick, Liquidity: L,
	}, nil
}
