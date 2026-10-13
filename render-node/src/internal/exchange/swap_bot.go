package exchange

import (
	"fmt"
	"math/big"
	"sync"
	"time"
)

// FlashSwap 閃兌：按當前市價（撮合對端/簿中價）快速兌換。
// 支援開放交易對的直接換算（TACM↔TiUSD、TACM↔USDT、TiUSD↔USDT），
// 不產生掛單；手續費沿用該交易對 taker 費率（TiUSD 報價最優惠）。
type FlashSwapResult struct {
	FromAsset  Asset  `json:"from_asset"`
	ToAsset    Asset  `json:"to_asset"`
	FromAmount Amount `json:"-"`
	ToAmount   Amount `json:"-"`
	Fee        Amount `json:"-"`
	Price      Amount `json:"-"`
	FromStr    string `json:"from_amount"`
	ToStr      string `json:"to_amount"`
	FeeStr     string `json:"fee"`
	PriceStr   string `json:"price"`
}

// FlashSwap 執行閃兌：from 資產賣出（以 from/to 交易對市價）→ 得 to 資產。
func (s *Service) FlashSwap(uid string, from, to Asset, amount Amount) (*FlashSwapResult, error) {
	if uid == "" || !from.Valid() || !to.Valid() || from == to || amount.Cmp(Amount{}) <= 0 {
		return nil, fmt.Errorf("exchange: 閃兌參數非法 (from=%s to=%s amount=%s)", from, to, amount.String(from))
	}
	m, err := marketFor(from, to)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 取得現價：有簿用最佳對端價，無簿用簿中價（若空簿則拒絕閃兌）。
	var price Amount
	ob := s.book(m)
	if from == m.Base {
		// 賣 base 買 quote：看買單（bids）最佳價（可賣價）。
		if b := ob.BestBid(); b != nil {
			price = *b
		}
	} else {
		// 賣 quote 買 base：看賣單（asks）最佳價（可買價）。
		if a := ob.BestAsk(); a != nil {
			price = *a
		}
	}
	if price.IsZero() {
		return nil, fmt.Errorf("exchange: %s 訂單簿無深度，無法閃兌（請先掛單）", m)
	}

	// 閃兌＝真實吃簿市價單：taker/maker 完整結算、手續費入 fee 帳戶。
	// （M19 E2E 實機驗證抓到原「直接 debit/credit 不碰訂單簿」守恆漏洞：maker 資產憑空消失。）
	var side Side
	var qty Amount
	if from == m.Base {
		// 賣 base 買 quote：市價賣單吃 bids。
		side = SideSell
		qty = amount
	} else {
		// 賣 quote 買 base：以最佳價折算 base 數量，市價買單吃 asks。
		side = SideBuy
		qty = divPrice(amount, price, m.Base.Decimals())
		if qty.Cmp(Amount{}) <= 0 {
			return nil, fmt.Errorf("exchange: 閃兌數量換算異常")
		}
	}
	taker := &Order{
		UID: uid, Market: m.String(), Side: side, Type: TypeMarket,
		Price: Amount{}, Qty: qty, QuoteBase: m.Base, QuoteQuote: m.Quote,
		CreatedTs: time.Now().Unix(),
	}
	trades, filledQty, err := s.matchTaker(taker, m, true)
	if err != nil {
		return nil, fmt.Errorf("exchange: 閃兌失敗（簿深度不足或狀態異常）: %w", err)
	}
	if filledQty.Cmp(Amount{}) <= 0 {
		return nil, fmt.Errorf("exchange: 閃兌無成交（簿無對端深度）")
	}
	// 彙總結算：所得資產與費用（taker 費已由 settleTrade 扣除）。
	var quoteSum, feeSum Amount
	for _, tr := range trades {
		quoteSum = quoteSum.Add(tr.Quote)
		feeSum = feeSum.Add(tr.TakerFee)
	}
	var toAmt Amount
	if side == SideSell {
		toAmt = quoteSum.Sub(feeSum) // 得 quote
	} else {
		toAmt = filledQty // 得 base
	}
	res := &FlashSwapResult{
		FromAsset: from, ToAsset: to, FromAmount: amount, ToAmount: toAmt, Fee: feeSum, Price: price,
	}
	res.FromStr = amount.String(from)
	res.ToStr = toAmt.String(to)
	res.FeeStr = feeSum.String(to)
	res.PriceStr = price.String(m.Quote)
	return res, nil
}

// marketFor 返回 from/to 對應交易對（from 與 to 之一為 base）。
func marketFor(from, to Asset) (Market, error) {
	cands := []Market{
		{AssetTACm, AssetTiUSD}, {AssetTACm, AssetUSDT}, {AssetTiUSD, AssetUSDT},
	}
	for _, m := range cands {
		if (m.Base == from && m.Quote == to) || (m.Quote == from && m.Base == to) {
			return m, nil
		}
	}
	return Market{}, fmt.Errorf("exchange: 不支援 %s↔%s 閃兌", from, to)
}

// divPrice 計算 quote 金額 → base 數量。
// 量綱：price 為「quote 最小單位 / 1 base 最小單位」；base_wei = quote_micro / price × 10^baseDec。
// M19 E2E 抓到：原實作用 quoteDec 縮放（差 10^(baseDec-quoteDec) 倍），「賣 quote 買 base」閃兌路徑受影響。
func divPrice(quoteAmt, price Amount, baseDec int) Amount {
	num := new(big.Int).Mul(quoteAmt.ToBig(), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(baseDec)), nil))
	return Amount{Big: new(big.Int).Div(num, price.ToBig())}
}

// ==================== 自動交易機器人（參照幣安自動交易模式） ====================

// BotConfig 為自動交易機器人配置：在指定交易對兩側定期掛買/賣單。
type BotConfig struct {
	UID             string `json:"uid"`
	Market          string `json:"market"`
	MidPrice        string `json:"mid_price"`         // 基準價（quote 最小單位十進制）
	SpreadPct       int    `json:"spread_pct"`        // 掛單價差（bps/2 分別為買/賣）
	Qty             string `json:"qty"`               // 每側掛單數量（base 最小單位十進制）
	IntervalSec     int    `json:"interval_sec"`      // 刷新間隔（秒）
	CancelOnRefresh bool   `json:"cancel_on_refresh"` // 每次刷新先撤舊單
}

// Bot 為一個運行中的自動交易機器人。
type Bot struct {
	ID        string    `json:"id"`
	Config    BotConfig `json:"config"`
	Status    string    `json:"status"` // running/stopped
	OrderIDs  []int64   `json:"order_ids"`
	Trades    int       `json:"trades"`
	StartedAt int64     `json:"started_at"`
	cancel    chan struct{}
	once      sync.Once
	mu        sync.Mutex
}

// BotStatus 為機器人運行視圖。
type BotStatus struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	Market    string  `json:"market"`
	MidPrice  string  `json:"mid_price"`
	SpreadBps int     `json:"spread_bps"`
	Qty       string  `json:"qty"`
	OrderIDs  []int64 `json:"order_ids"`
	Trades    int     `json:"trades"`
	StartedAt int64   `json:"started_at"`
}

// StartBot 啟動自動交易機器人（Goroutine 定期掛買/賣單）。
func (s *Service) StartBot(cfg BotConfig) (*Bot, error) {
	if cfg.UID == "" {
		return nil, fmt.Errorf("exchange: 機器人需要 uid")
	}
	m, err := ParseMarket(cfg.Market)
	if err != nil || !m.Supported() {
		return nil, fmt.Errorf("exchange: 機器人交易對非法: %v", err)
	}
	mid, err := NewAmount(cfg.MidPrice, m.Quote)
	if err != nil || mid.Cmp(Amount{}) <= 0 {
		return nil, fmt.Errorf("exchange: 基準價非法")
	}
	qty, err := NewAmount(cfg.Qty, m.Base)
	if err != nil || qty.Cmp(Amount{}) <= 0 {
		return nil, fmt.Errorf("exchange: 數量非法")
	}
	if cfg.SpreadPct <= 0 {
		cfg.SpreadPct = 20 // 預設 0.2%（20bp，兩側各 10bp）
	}
	if cfg.IntervalSec <= 0 {
		cfg.IntervalSec = 10
	}

	id := fmt.Sprintf("bot-%d", time.Now().UnixNano())
	bot := &Bot{
		ID: id, Config: cfg, Status: "running",
		StartedAt: time.Now().Unix(), cancel: make(chan struct{}),
	}
	s.mu.Lock()
	s.bots[id] = bot
	s.mu.Unlock()

	// 第一次立即掛單。
	s.placeBotOrders(bot, m, mid, qty)
	go s.botLoop(bot, m, mid, qty)
	return bot, nil
}

// StopBot 停止機器人（撤掉其剩餘訂單）。
func (s *Service) StopBot(id string) (*BotStatus, error) {
	s.mu.Lock()
	bot, ok := s.bots[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("exchange: 機器人 %s 不存在", id)
	}
	bot.once.Do(func() { close(bot.cancel) })
	bot.mu.Lock()
	ids := append([]int64{}, bot.OrderIDs...)
	bot.mu.Unlock()
	for _, oid := range ids {
		_, _ = s.CancelOrder(bot.Config.UID, oid)
	}
	bot.Status = "stopped"
	return bot.StatusView(), nil
}

// BotStatusView 返回機器人狀態。
func (b *Bot) StatusView() *BotStatus {
	b.mu.Lock()
	ids := append([]int64{}, b.OrderIDs...)
	tr := b.Trades
	b.mu.Unlock()
	return &BotStatus{
		ID: b.ID, Status: b.Status, Market: b.Config.Market,
		MidPrice: b.Config.MidPrice, SpreadBps: b.Config.SpreadPct,
		Qty: b.Config.Qty, OrderIDs: ids,
		Trades: tr, StartedAt: b.StartedAt,
	}
}

// ListBots 返回全部機器人。
func (s *Service) ListBots() []*BotStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*BotStatus{}
	for _, b := range s.bots {
		out = append(out, b.StatusView())
	}
	return out
}

// botLoop 定期刷新掛單。
func (s *Service) botLoop(bot *Bot, m Market, mid, qty Amount) {
	ticker := time.NewTicker(time.Duration(bot.Config.IntervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-bot.cancel:
			return
		case <-ticker.C:
			bot.mu.Lock()
			if bot.Config.CancelOnRefresh {
				for _, oid := range bot.OrderIDs {
					_, _ = s.CancelOrder(bot.Config.UID, oid)
				}
				bot.OrderIDs = nil
			}
			s.placeBotOrders(bot, m, mid, qty)
			bot.mu.Unlock()
		}
	}
}

// placeBotOrders 在簿兩側掛買/賣單（價差由 spread 對半）。
func (s *Service) placeBotOrders(bot *Bot, m Market, mid, qty Amount) {
	half := big.NewInt(int64(bot.Config.SpreadPct / 2))
	div := big.NewInt(10000)
	// 買價 = mid × (1 - half/10000)；賣價 = mid × (1 + half/10000)。
	buyP := Amount{Big: new(big.Int).Mul(mid.ToBig(), new(big.Int).Sub(div, half))}
	buyP = Amount{Big: new(big.Int).Div(buyP.ToBig(), div)}
	sellP := Amount{Big: new(big.Int).Mul(mid.ToBig(), new(big.Int).Add(div, half))}
	sellP = Amount{Big: new(big.Int).Div(sellP.ToBig(), div)}

	if buyP.Cmp(Amount{}) > 0 {
		if res, err := s.PlaceOrder(bot.Config.UID, m, SideBuy, TypeLimit, buyP, qty); err == nil {
			bot.OrderIDs = append(bot.OrderIDs, res.Order.ID)
			bot.Trades += len(res.Trades)
		}
	}
	if sellP.Cmp(Amount{}) > 0 {
		if res, err := s.PlaceOrder(bot.Config.UID, m, SideSell, TypeLimit, sellP, qty); err == nil {
			bot.OrderIDs = append(bot.OrderIDs, res.Order.ID)
			bot.Trades += len(res.Trades)
		}
	}
}
