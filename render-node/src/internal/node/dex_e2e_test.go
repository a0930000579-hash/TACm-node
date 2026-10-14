package node

import (
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 本檔驗證 M45 鏈上 DEX（AMM）閉環（真實 VM 代幣）：
//  1. 部署兩個標準代幣（TKA/TKB）
//  2. POST /dex/create 建池（池帳戶 DeriveKey 派生）
//  3. POST /dex/liquidity/add 注入流動性（LP 份額）
//  4. POST /dex/swap 即時兌換（恆定乘積＋0.3% 手續費），餘額鏈上變動
//  5. GET /dex/quote 報價一致
//  6. POST /dex/liquidity/remove 退回流動性

func dexSetup(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	_, ts, _ := contractStudioSetup(t)
	// 部署兩個代幣。
	r0 := studioPostJSON(t, ts, "/contract/deploy", map[string]any{
		"name": "TokenA", "symbol": "TKA", "supply": "1000000000000",
	})
	if r0["ok"] != true {
		t.Fatalf("部署 TKA 失敗: %v", r0)
	}
	r1 := studioPostJSON(t, ts, "/contract/deploy", map[string]any{
		"name": "TokenB", "symbol": "TKB", "supply": "1000000000000",
	})
	if r1["ok"] != true {
		t.Fatalf("部署 TKB 失敗: %v", r1)
	}
	// 等待交易入塊（合約真實部署）。
	tkA := r0["contract_address"].(string)
	tkB := r1["contract_address"].(string)
	waitFor(t, func() bool {
		l := studioGetJSON(t, ts, "/contract/list")
		infos, _ := l["contracts"].([]any)
		return len(infos) >= 2
	}, 15*time.Second, "兩個代幣合約入塊")
	return ts, tkA, tkB
}

func TestDEXCreateAndPools(t *testing.T) {
	ts, tkA, tkB := dexSetup(t)
	r := studioPostJSON(t, ts, "/dex/create", map[string]any{
		"token0": tkA, "token1": tkB,
	})
	if r["ok"] != true {
		t.Fatalf("建池失敗: %v", r)
	}
	pool, _ := r["pool"].(map[string]any)
	if pool["id"] != "p1" || pool["token0"] != tkA || pool["token1"] != tkB {
		t.Fatalf("池欄位異常: %v", pool)
	}
	if pool["pool_addr"] == "" {
		t.Fatal("池帳戶地址缺失")
	}
	// 重複建池應失敗。
	r2 := studioPostJSON(t, ts, "/dex/create", map[string]any{
		"token0": tkB, "token1": tkA,
	})
	if r2["ok"] == true {
		t.Fatal("重複交易對應被拒絕")
	}
	// 列表含池。
	l := studioGetJSON(t, ts, "/dex/pools")
	if l["ok"] != true {
		t.Fatalf("池列表失敗: %v", l)
	}
}

func TestDEXLiquidityAndSwapE2E(t *testing.T) {
	ts, tkA, tkB := dexSetup(t)
	// 建池。
	r := studioPostJSON(t, ts, "/dex/create", map[string]any{
		"token0": tkA, "token1": tkB,
	})
	if r["ok"] != true {
		t.Fatalf("建池失敗: %v", r)
	}
	// 注入 100000 / 50000。
	r = studioPostJSON(t, ts, "/dex/liquidity/add", map[string]any{
		"pool": "p1", "amount0": "100000", "amount1": "50000",
	})
	if r["ok"] != true {
		t.Fatalf("注入流動性失敗: %v", r)
	}
	pool, _ := r["pool"].(map[string]any)
	if pool["reserve0"] != "100000" || pool["reserve1"] != "50000" {
		t.Fatalf("儲備異常: %v", pool)
	}
	pos, _ := r["position"].(map[string]any)
	if pos["shares"] == "" || pos["shares"] == "0" {
		t.Fatalf("LP 份額異常: %v", pos)
	}
	// 驗證 admin 餘額已扣（鏈上）與池餘額入帳。
	info := studioGetJSON(t, ts, "/contract/erc20/"+tkA+"?holder="+pool["pool_addr"].(string))
	if info["ok"] != true {
		t.Fatalf("讀池餘額失敗: %v", info)
	}
	if info["balance"] != "100000" {
		t.Fatalf("池 TKA 餘額應為 100000，得到 %v", info["balance"])
	}

	// 報價：1000 TKA 換 TKB。
	q := studioGetJSON(t, ts, "/dex/quote?pool=p1&token="+tkA+"&amount=1000")
	if q["ok"] != true {
		t.Fatalf("報價失敗: %v", q)
	}
	amtOut := q["amount_out"].(string)
	if amtOut == "" || amtOut == "0" {
		t.Fatalf("報價為 0: %v", q)
	}
	// 手動驗算：in'=997；out = 50000 - (100000*50000)/(100000+997)；
	// 5000000000/100997 = 49506 → out = 494。
	if amtOut != "494" {
		t.Fatalf("報價應為 494 TKB，得到 %s", amtOut)
	}

	// Swap 1000 TKA → 494 TKB。
	s := studioPostJSON(t, ts, "/dex/swap", map[string]any{
		"pool": "p1", "token": tkA, "amount": "1000",
	})
	if s["ok"] != true {
		t.Fatalf("swap 失敗: %v", s)
	}
	if s["token_out"] != tkB || s["amount_out"] != "494" {
		t.Fatalf("swap 異常: %v", s)
	}
	// 池儲備更新。
	p, _ := s["pool"].(map[string]any)
	if p["reserve0"] != "101000" || p["reserve1"] != "49506" {
		t.Fatalf("swap 後儲備異常: %v", p)
	}

	// 退出流動性：全部份額。
	sh := pos["shares"].(string)
	rm := studioPostJSON(t, ts, "/dex/liquidity/remove", map[string]any{
		"pool": "p1", "shares": sh,
	})
	if rm["ok"] != true {
		t.Fatalf("退出流動性失敗: %v", rm)
	}
	pool2, _ := rm["pool"].(map[string]any)
	// 剩餘儲備應遠小於注入（大部分已退出；swap 手續費/滑價留池）。
	if pool2["reserve0"] == "101000" {
		t.Fatalf("退出後儲備應減少: %v", pool2)
	}
	pos2, _ := rm["position"].(map[string]any)
	if pos2["shares"] != "0" {
		t.Fatalf("退出後份額應為 0，得到 %v", pos2["shares"])
	}
}

func TestDEXSwapRejectsUnknown(t *testing.T) {
	ts, tkA, tkB := dexSetup(t)
	if r := studioPostJSON(t, ts, "/dex/create", map[string]any{
		"token0": tkA, "token1": tkB,
	}); r["ok"] != true {
		t.Fatalf("建池失敗: %v", r)
	}
	// 未注入即 swap → 失敗。
	s := studioPostJSON(t, ts, "/dex/swap", map[string]any{
		"pool": "p1", "token": tkA, "amount": "100",
	})
	if s["ok"] == true {
		t.Fatal("無流動性 swap 應失敗")
	}
	// 未知池。
	s2 := studioPostJSON(t, ts, "/dex/swap", map[string]any{
		"pool": "p9", "token": tkA, "amount": "100",
	})
	if s2["ok"] == true {
		t.Fatal("未知池應失敗")
	}
	// 不存在的代幣合約建池。
	bad := studioPostJSON(t, ts, "/dex/create", map[string]any{
		"token0": "tx0notexist0000000000000000000000000000000000",
		"token1": tkB,
	})
	if bad["ok"] == true {
		t.Fatal("不存在的代幣應拒絕建池")
	}
	// JSON 回應格式（大數值以字串）。
	var _ = json.Marshal
	var _ = big.NewInt
	var _ = strings.TrimSpace
}
