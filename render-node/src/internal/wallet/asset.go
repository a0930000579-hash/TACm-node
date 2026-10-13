// Package wallet 實現 TAC Ai 智能鏈的託管錢包與資產帳本（M13 業務層第一步）。
// 多資產（TACm 原生幣、TiUSD 穩定幣、USDT 估值對映）：TACm 使用 big.Int
// （1e18 wei，避免 coinbase/大額轉帳溢出 int64），TiUSD/USDT 使用 int64
// micro（1e6）。每筆操作為雙分錄（sum(delta)=0）並寫入審計帳本；
// 手續費以 basis points 定點計算。
package wallet

import (
	"fmt"
	"math/big"
	"strings"
)

// Asset 為支持的資產類別。
type Asset string

const (
	// AssetTACm 原生幣（18 位小數，big.Int 精度）。
	AssetTACm Asset = "TACm"
	// AssetTiUSD 穩定幣（6 位小數，peg 1 USDT）。
	AssetTiUSD Asset = "TIUSD"
	// AssetUSDT USDT 對映（6 位小數，估值/計價）。
	AssetUSDT Asset = "USDT"
)

// Decimals 返回資產的小數位數（最小單位換算）。
func (a Asset) Decimals() int {
	switch a {
	case AssetTACm:
		return 18
	case AssetTiUSD, AssetUSDT:
		return 6
	default:
		return 0
	}
}

// Valid 報告資產是否受支持。
// NormalizeAsset 大小寫不敏感解析資產名（TACM/TiUSD/TIUSD/USDT/tiusd…）。
func NormalizeAsset(s string) (Asset, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TACM":
		return AssetTACm, true
	case "TIUSD", "TUSD":
		return AssetTiUSD, true
	case "USDT":
		return AssetUSDT, true
	default:
		return "", false
	}
}

func (a Asset) Valid() bool {
	switch a {
	case AssetTACm, AssetTiUSD, AssetUSDT:
		return true
	default:
		return false
	}
}

// FeeBps 返回該資產的站內轉帳手續費率（basis points，1/10000）。
// 商務規則：TiUSD 0.5%（50bp）最優惠以吸引穩定幣使用；USDT 1.25%（125bp）；
// TACm 2%（200bp）。
func (a Asset) FeeBps() int {
	switch a {
	case AssetTiUSD:
		return 50
	case AssetUSDT:
		return 125
	case AssetTACm:
		return 200
	default:
		return 0
	}
}

var (
	big10000 = big.NewInt(10000)
	big9999  = big.NewInt(9999)
)

// FeeOf 以向上取整計算手續費（amount 為最小單位整數；big.Int 防溢出）。
// fee = ceil(amount × bps / 10000)。
func FeeOf(amount *big.Int, bps int) *big.Int {
	if amount == nil || amount.Sign() <= 0 || bps <= 0 {
		return new(big.Int)
	}
	n := new(big.Int).Mul(amount, big.NewInt(int64(bps)))
	n.Add(n, big9999)
	return new(big.Int).Div(n, big10000)
}

// Amount 以最小單位整數表示資產金額（TACm 用 big.Int，穩定幣用 int64）。
type Amount struct {
	Big *big.Int // TACm：1e18 單位；穩定幣：與 I64 互斥使用
	I64 int64    // TiUSD/USDT：1e6 單位
}

// NewAmount 從十進制金額字符串解析為最小單位整數（任意精度）。
func NewAmount(s string, asset Asset) (Amount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Amount{}, fmt.Errorf("金額為空")
	}
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	parts := strings.SplitN(s, ".", 2)
	whole, frac := parts[0], ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if whole == "" {
		whole = "0"
	}
	for _, c := range whole {
		if c < '0' || c > '9' {
			return Amount{}, fmt.Errorf("金額含非法字符: %q", s)
		}
	}
	if len(frac) > asset.Decimals() {
		return Amount{}, fmt.Errorf("金額精度超出 %d 位小數: %s", asset.Decimals(), s)
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return Amount{}, fmt.Errorf("金額含非法字符: %q", s)
		}
	}
	frac += strings.Repeat("0", asset.Decimals()-len(frac))
	digits := whole + frac
	if digits == "" {
		digits = "0"
	}
	// 去掉前導零。
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		digits = "0"
	}
	bigv, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Amount{}, fmt.Errorf("金額非法: %q", s)
	}
	if neg {
		bigv.Neg(bigv)
	}
	if bigv.Sign() < 0 {
		return Amount{}, fmt.Errorf("金額不能為負: %s", s)
	}
	if asset == AssetTACm {
		return Amount{Big: bigv}, nil
	}
	// 穩定幣：驗證在 int64 範圍內（上限 9.2e12 單位）。
	if !bigv.IsInt64() {
		return Amount{}, fmt.Errorf("金額超出 int64 範圍: %s", s)
	}
	return Amount{I64: bigv.Int64()}, nil
}

// NewAmountF 從 float64 構造金額（%.6f 格式化；非法輸入回傳零值，實務不會發生）。
func NewAmountF(f float64, asset Asset) Amount {
	a, err := NewAmount(fmt.Sprintf("%.6f", f), asset)
	if err != nil {
		return Amount{}
	}
	return a
}

// IsZero 判斷金額是否為零。
func (a Amount) IsZero() bool {
	if a.Big != nil {
		return a.Big.Sign() == 0
	}
	return a.I64 == 0
}

// String 返回十進制顯示（最小單位 → 帶小數，去尾零）。
func (a Amount) String(asset Asset) string {
	if asset == AssetTACm {
		return formatBig(a.Big, asset.Decimals())
	}
	return formatI64(a.I64, asset.Decimals())
}

// FormatAmountBig 格式化 big.Int（TACm 1e18 單位）為十進制字符串。
func FormatAmountBig(v *big.Int) string { return formatBig(v, AssetTACm.Decimals()) }

// FormatAmountI64 格式化 int64（穩定幣 1e6 單位）為十進制字符串。
func FormatAmountI64(v int64, asset Asset) string { return formatI64(v, asset.Decimals()) }

// Cmp 比較兩金額（跨資產僅用於同資產比較；nil big 視為 0）。
func (a Amount) Cmp(b Amount) int {
	if a.Big != nil || b.Big != nil {
		return bigOrZero(a.Big).Cmp(bigOrZero(b.Big))
	}
	if a.I64 < b.I64 {
		return -1
	}
	if a.I64 > b.I64 {
		return 1
	}
	return 0
}

// Add/Sub 返回新金額（同資產）。
func (a Amount) Add(b Amount) Amount {
	if a.Big != nil || b.Big != nil {
		x := bigOrZero(a.Big)
		return Amount{Big: new(big.Int).Add(x, bigOrZero(b.Big))}
	}
	return Amount{I64: a.I64 + b.I64}
}

func (a Amount) Sub(b Amount) Amount {
	if a.Big != nil || b.Big != nil {
		x := bigOrZero(a.Big)
		return Amount{Big: new(big.Int).Sub(x, bigOrZero(b.Big))}
	}
	return Amount{I64: a.I64 - b.I64}
}

func bigOrZero(b *big.Int) *big.Int {
	if b == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(b)
}

func formatBig(v *big.Int, dec int) string {
	if v == nil {
		v = new(big.Int)
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
		fs := fmt.Sprintf("%0*s", dec, frac.String())
		fs = strings.TrimRight(fs, "0")
		s += "." + fs
	}
	if neg {
		s = "-" + s
	}
	return s
}

func formatI64(v int64, dec int) string {
	neg := v < 0
	if neg {
		v = -v
	}
	div := pow10(dec)
	whole := v / div
	frac := v % div
	if frac == 0 {
		s := fmt.Sprintf("%d", whole)
		if neg {
			s = "-" + s
		}
		return s
	}
	fs := fmt.Sprintf("%0*d", dec, frac)
	fs = strings.TrimRight(fs, "0")
	s := fmt.Sprintf("%d.%s", whole, fs)
	if neg {
		s = "-" + s
	}
	return s
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}
