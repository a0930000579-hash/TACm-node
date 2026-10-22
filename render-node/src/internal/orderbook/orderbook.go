// Package orderbook implements a chain-verifiable on-chain orderbook spot exchange.
//
// 與 internal/exchange（中心化撮合，內部記帳簿）的差異：
//   - 每一張掛單／成交／撤單都是鏈上狀態事件，公開可查（SQLite orderbook.db，
//     WAL、單連線、任何人可驗證）。
//   - 委託資金在掛單時由 RPC 層鎖定轉入「orderbook_escrow」鏈上託管帳戶；
//     撤單時解鎖退還；成交時在買賣方與 escrow 間確定性結算（含手續費）。
//   - 撮合規則為確定性的價格-時間優先（price-time priority）：買單價高者先、
//     賣單價低者先；同價以掛單先後（id）為序。無中心化內部撮合流程。
//   - 本包只維護純狀態與持久化；實際 TACm/TiUSD 資金移轉由
//     internal/node/orderbook_rpc.go 透過 walletSvc 完成（對標 aimarket 分工）。
package orderbook

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// 常數：費率與託管帳戶。
const (
	// FeeAccount 平台手續費入帳帳戶。
	FeeAccount = "orderbook_fee"
	// EscrowAccount 掛單託管帳戶（RPC 層把用戶委託額 Transfer 到此）。
	EscrowAccount = "orderbook_escrow"
	// DefaultMakerRate maker 費率（成交金額 quote 部分的比例）。
	// 預設 0：maker 免費，吸引流動性。
	DefaultMakerRate = 0.0
	// DefaultTakerRate taker 費率（0.1%）。
	DefaultTakerRate = 0.001
)

// 訂單 side / type / status。
const (
	SideBuy  = "buy"
	SideSell = "sell"

	TypeLimit = "limit"

	StatusOpen     = "open"
	StatusPartial  = "partial"
	StatusFilled   = "filled"
	StatusCanceled = "canceled"
)

// Market 交易對。
type Market struct {
	Symbol    string  `json:"symbol"`     // e.g. "TACm/TiUSD"
	Base      string  `json:"base"`       // e.g. "TACm"
	Quote     string  `json:"quote"`      // e.g. "TiUSD"
	MinAmount float64 `json:"min_amount"` // 最小 base 掛單量
	PriceTick float64 `json:"price_tick"` // 最小價格變動（quote per base）
}

// Order 一張限價掛單。
type Order struct {
	ID        int64   `json:"id"`
	Market    string  `json:"market"`
	Maker     string  `json:"maker"`  // 下單地址（正規化 tx0 形式）
	Side      string  `json:"side"`   // buy | sell
	Type      string  `json:"type"`   // limit
	Price     float64 `json:"price"`  // quote per base
	Amount    float64 `json:"amount"` // base
	Filled    float64 `json:"filled"` // base
	Fee       float64 `json:"fee"`    // 累計手續費（quote，僅本單作 taker 時累加）
	Status    string  `json:"status"` // open | partial | filled | canceled
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

// Remaining 尚未成交 base 量。
func (o *Order) Remaining() float64 { return o.Amount - o.Filled }

// Fill 一筆成交（taker 與 resting maker 撮合成交）。
type Fill struct {
	ID           int64   `json:"id"`
	Market       string  `json:"market"`
	Taker        string  `json:"taker"`
	Maker        string  `json:"maker"`
	TakerOrderID int64   `json:"taker_order_id"`
	MakerOrderID int64   `json:"maker_order_id"`
	TakerSide    string  `json:"taker_side"` // buy | sell
	Price        float64 `json:"price"`      // 成交價（maker 掛單價）
	Amount       float64 `json:"amount"`     // base 量
	TakerFee     float64 `json:"taker_fee"`  // quote
	MakerFee     float64 `json:"maker_fee"`  // quote
	Time         int64   `json:"time"`
}

// DepthLevel 訂單簿一個聚合價位。
type DepthLevel struct {
	Price      float64 `json:"price"`
	Amount     float64 `json:"amount"` // base
	OrderCount int     `json:"order_count"`
}

// Depth 買賣深度快照。
type Depth struct {
	Market string       `json:"market"`
	Bids   []DepthLevel `json:"bids"` // 高→低
	Asks   []DepthLevel `json:"asks"` // 低→高
}

// PlaceResult PlaceOrder 的回執：新單（可能部分成交）＋逐筆成交＋剩餘掛單量。
type PlaceResult struct {
	Order     *Order  `json:"order"`
	Fills     []Fill  `json:"fills"`
	Remaining float64 `json:"remaining"` // base 量，仍掛在簿上等待成交
}

// Store orderbook 持久化（SQLite WAL、單連線、sync.Mutex）。
type Store struct {
	db        *sql.DB
	mu        sync.Mutex
	makerRate float64
	takerRate float64
	markets   map[string]Market
}

// Open 開啟 orderbook.db 並建表／種子市場。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("orderbook: 建立數據目錄: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		filepath.Join(dataDir, "orderbook.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("orderbook: 開啟數據庫: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("orderbook: 初始化 schema: %w", err)
	}
	s := &Store{
		db:        db,
		makerRate: DefaultMakerRate,
		takerRate: DefaultTakerRate,
		markets:   seedMarkets(),
	}
	// 種子市場寫入 ob_markets（IF NOT EXISTS，重啟可保留自定義）。
	for _, m := range s.markets {
		if _, err := db.Exec(`INSERT OR IGNORE INTO ob_markets(symbol,base,quote,min_amount,price_tick) VALUES(?,?,?,?,?)`,
			m.Symbol, m.Base, m.Quote, m.MinAmount, m.PriceTick); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("orderbook: 寫入市場 %s: %w", m.Symbol, err)
		}
	}
	return s, nil
}

// Close 關閉資料庫。
func (s *Store) Close() error { return s.db.Close() }

// SetFeeRates 動態調整 maker/taker 費率（營運／治理用）。
func (s *Store) SetFeeRates(maker, taker float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maker >= 0 && maker < 0.05 {
		s.makerRate = maker
	}
	if taker >= 0 && taker < 0.05 {
		s.takerRate = taker
	}
}

// FeeRates 回傳目前 (maker, taker) 費率。
func (s *Store) FeeRates() (float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.makerRate, s.takerRate
}

// seedMarkets 預設交易對。
func seedMarkets() map[string]Market {
	m := map[string]Market{}
	for _, x := range []Market{
		{Symbol: "TACm/TiUSD", Base: "TACm", Quote: "TiUSD", MinAmount: 0.001, PriceTick: 0.0001},
	} {
		m[x.Symbol] = x
	}
	return m
}

// NormalizeMarket 正規化市場符號：大小寫不敏感，比對已知市場後回傳 canonical 形式。
// 未知市場（不在種子／DB）則回傳 BASE/QUOTE（去空白、保留大小寫原樣）。
func (s *Store) NormalizeMarket(market string) (string, error) {
	market = strings.TrimSpace(market)
	if market == "" {
		return "", fmt.Errorf("orderbook: 缺少市場")
	}
	sep := "/"
	if strings.Contains(market, "-") && !strings.Contains(market, "/") {
		sep = "-"
	}
	parts := strings.SplitN(market, sep, 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("orderbook: 市場格式應為 BASE/QUOTE (got %q)", market)
	}
	base := strings.TrimSpace(parts[0])
	quote := strings.TrimSpace(parts[1])
	upper := strings.ToUpper(base) + "/" + strings.ToUpper(quote)
	s.mu.Lock()
	defer s.mu.Unlock()
	for sym := range s.markets {
		if strings.ToUpper(sym) == upper {
			return sym, nil
		}
	}
	// DB 中自定義市場（大小寫不敏感）。
	var canonical string
	err := s.db.QueryRow(`SELECT symbol FROM ob_markets WHERE UPPER(symbol)=?`, upper).Scan(&canonical)
	if err == nil {
		return canonical, nil
	}
	return base + "/" + quote, nil
}
