// Package exchange 實現 TAC Ai 智能鏈的交易所核心（M14 業務層第二步）。
// 對照 Python 源碼 exchange_match.py / exchange_db.py / exchange_finance.py 重寫：
//   - 價格-時間優先的限價/市價撮合（多交易對：TACM/TIUSD、TACM/USDT、TIUSD/USDT）
//   - 多資產餘額簿（avail 可用 / locked 鎖定，撮合時先鎖後結算）
//   - 差異化手續費：quote 為 TiUSD 時 taker 0.5%/maker 0.25%（最優惠）、
//     USDT 1.25%/0.625%、TACm 0.1%/0.05%
//   - 閃兌（FlashSwap）：按當前市價快速兌換開放交易對
//   - 自動交易機器人（Bot）：參照幣安自動交易模式，於簿兩側定期掛買/賣單
//
// 撮合在交易所內部資產簿結算（avail/locked），與鏈上錢包透過充值/提領橋接。
package exchange

import (
	"fmt"
	"math/big"
	"strings"
)

// Asset 為交易所支持的資產。
type Asset string

const (
	AssetTACm  Asset = "TACm"
	AssetTiUSD Asset = "TiUSD"
	AssetUSDT  Asset = "USDT"
)

// Valid 報告資產是否支持（M104：含主流幣參考資產 BTC/ETH/BNB/SOL/ADA）。
func (a Asset) Valid() bool {
	switch a {
	case AssetTACm, AssetTiUSD, AssetUSDT:
		return true
	default:
		return IsRefBase(a)
	}
}

// Decimals 返回資產小數位。
func (a Asset) Decimals() int {
	switch a {
	case AssetTACm:
		return 18
	default:
		return 6
	}
}

// Market 為交易對（base/quote）。
type Market struct {
	Base  Asset
	Quote Asset
}

// normAsset 正規化資產名（接受 TACM/tacm/TiUSD/TIUSD/USDT 等大小寫變體）。
func normAsset(s string) (Asset, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TACM":
		return AssetTACm, true
	case "TIUSD", "TUSD", "TUSD2":
		return AssetTiUSD, true
	case "USDT", "USDT2":
		return AssetUSDT, true
	default:
		return "", false
	}
}

// NormAssetEx 導出資產正規化（大小寫不敏感）。
func NormAssetEx(s string) (Asset, bool) { return normAsset(s) }

// ParseMarket 從 "BASE/QUOTE" 解析交易對（大小寫不敏感）。
func ParseMarket(s string) (Market, error) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 {
		return Market{}, fmt.Errorf("交易對格式錯誤: %q（需 BASE/QUOTE）", s)
	}
	base, ok1 := normAsset(parts[0])
	quote, ok2 := normAsset(parts[1])
	if !ok1 || !ok2 {
		return Market{}, fmt.Errorf("不支持的資產: %q", s)
	}
	if base == quote {
		return Market{}, fmt.Errorf("交易對 base=quote: %q", s)
	}
	return Market{Base: base, Quote: quote}, nil
}

// String 返回 "BASE/QUOTE"。
func (m Market) String() string { return string(m.Base) + "/" + string(m.Quote) }

// Supported 報告該交易對是否開放。
func (m Market) Supported() bool {
	switch m {
	case Market{AssetTACm, AssetTiUSD},
		Market{AssetTACm, AssetUSDT},
		Market{AssetTiUSD, AssetUSDT}:
		return true
	default:
		return false
	}
}

// FeeRates 交易對手續費（ppm：百萬分之一，支援 0.625% 等非整數 bp）。
type FeeRates struct {
	TakerPpm int64
	MakerPpm int64
}

// FeeRates 返回交易對費率（ppm）：TiUSD 5000/2500、USDT 12500/6250、其餘 1000/500。
func (m Market) FeeRates() FeeRates {
	switch m.Quote {
	case AssetTiUSD:
		return FeeRates{TakerPpm: 5000, MakerPpm: 2500} // 0.5% / 0.25%
	case AssetUSDT:
		return FeeRates{TakerPpm: 12500, MakerPpm: 6250} // 1.25% / 0.625%
	default:
		return FeeRates{TakerPpm: 1000, MakerPpm: 500} // 0.1% / 0.05%
	}
}

// Side 為訂單方向。
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

// Valid 報告方向是否合法。
func (s Side) Valid() bool { return s == SideBuy || s == SideSell }

// OrderType 為訂單類型。
type OrderType string

const (
	TypeLimit  OrderType = "limit"
	TypeMarket OrderType = "market"
)

// Valid 報告訂單類型是否合法。
func (t OrderType) Valid() bool { return t == TypeLimit || t == TypeMarket }

// OrderStatus 為訂單狀態。
type OrderStatus string

const (
	StatusOpen            OrderStatus = "open"
	StatusPartiallyFilled OrderStatus = "partially_filled"
	StatusFilled          OrderStatus = "filled"
	StatusCanceled        OrderStatus = "canceled"
)

// Amount 以最小單位整數表示資產數量（TACm 18 位 big.Int、穩定幣 6 位 int64）。
// 交易所內部一律用最小單位整數計算，避免浮點精度問題（Go 慣用）。
type Amount struct {
	Big *big.Int // TACm
	I64 int64    // TiUSD / USDT
}

// NewAmount 解析十進制金額為最小單位。
func NewAmount(s string, a Asset) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Amount{}, fmt.Errorf("金額為空")
	}
	dec := a.Decimals()
	parts := strings.SplitN(s, ".", 2)
	whole, frac := parts[0], ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > dec {
		return Amount{}, fmt.Errorf("精度超出 %d 位: %s", dec, s)
	}
	frac += strings.Repeat("0", dec-len(frac))
	digits := strings.TrimLeft(whole+frac, "0")
	if digits == "" {
		digits = "0"
	}
	v, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Amount{}, fmt.Errorf("金額非法: %q", s)
	}
	if a == AssetTACm {
		return Amount{Big: v}, nil
	}
	if !v.IsInt64() {
		return Amount{}, fmt.Errorf("金額超出範圍: %s", s)
	}
	return Amount{I64: v.Int64()}, nil
}

// toBig 返回統一的 big.Int 表示（單一欄位優先；I64-only 轉 big）。
func (a Amount) toBig() *big.Int {
	if a.Big != nil {
		return a.Big
	}
	return big.NewInt(a.I64)
}

// IsZero 報告金額是否為 0。
func (a Amount) IsZero() bool { return a.toBig().Sign() == 0 }

// Cmp 比較兩金額（混用 Big/I64 亦正確）。
func (a Amount) Cmp(b Amount) int { return a.toBig().Cmp(b.toBig()) }

// Add 返回和（回傳 big 表示，避免精度丟失）。
func (a Amount) Add(b Amount) Amount {
	return Amount{Big: new(big.Int).Add(a.toBig(), b.toBig())}
}

// Sub 返回差。
func (a Amount) Sub(b Amount) Amount {
	return Amount{Big: new(big.Int).Sub(a.toBig(), b.toBig())}
}

// MulPpm 返回 amount×ppm/1,000,000（向上取整，用於手續費）。
func (a Amount) MulPpm(ppm int64) Amount {
	if ppm <= 0 {
		return Amount{}
	}
	b := big.NewInt(ppm)
	div := big.NewInt(1000000)
	if a.Big != nil {
		n := new(big.Int).Mul(a.Big, b)
		n.Add(n, big.NewInt(999999))
		return Amount{Big: new(big.Int).Div(n, div)}
	}
	// int64 安全：ppm ≤ 1e6，i64 ≤ 9.2e12 → 乘積 ≤ 9.2e18 < 9.2e18。
	n := a.I64 * ppm
	if n < 0 {
		return Amount{}
	}
	return Amount{I64: (n + 999999) / 1000000}
}

// String 返回十進制顯示（去尾零）。
func (a Amount) String(asset Asset) string {
	dec := asset.Decimals()
	if a.Big != nil {
		return formatBig(a.Big, dec)
	}
	return formatI64(a.I64, dec)
}

// Float 返回浮點近似（僅用於展示/外部兼容，內部計算一律用整數）。
func (a Amount) Float(asset Asset) float64 {
	f := new(big.Float).SetInt(a.ToBig())
	div := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(asset.Decimals())), nil))
	f.Quo(f, div)
	v, _ := f.Float64()
	return v
}

// ToBig 返回 big.Int 表示（統一介面）。
func (a Amount) ToBig() *big.Int {
	if a.Big != nil {
		return a.Big
	}
	return big.NewInt(a.I64)
}

func formatBig(v *big.Int, dec int) string {
	if v == nil || v.Sign() == 0 {
		return "0"
	}
	neg := v.Sign() < 0
	if neg {
		v = new(big.Int).Neg(v)
	}
	div := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil)
	whole := new(big.Int).Div(v, div)
	frac := new(big.Int).Mod(v, div)
	s := whole.String()
	if frac.Sign() != 0 {
		fs := fmt.Sprintf("%0*d", dec, frac)
		fs = strings.TrimRight(fs, "0")
		s += "." + fs
	}
	if neg {
		s = "-" + s
	}
	return s
}

func formatI64(v int64, dec int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	div := int64(1)
	for i := 0; i < dec; i++ {
		div *= 10
	}
	whole, frac := v/div, v%div
	s := fmt.Sprintf("%d", whole)
	if frac != 0 {
		s += "." + strings.TrimRight(fmt.Sprintf("%0*d", dec, frac), "0")
	}
	if neg {
		s = "-" + s
	}
	return s
}
