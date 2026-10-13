package node

import (
	"math/big"
	"net/http/httptest"
	"testing"
	"time"
)

// 本檔驗證 M46 鏈上流動性挖礦（LP 質押＋獎勵分潤）：
//  1. 部署 TKA（獎勵代幣）＋TKB、建池、注入流動性（admin 持 LP）
//  2. POST /dex/stake/create 建農場（每池一個、重複拒絕）
//  3. POST /dex/stake/fund 注資獎勵代幣至金庫（鏈上轉帳）
//  4. 出塊後 POST /dex/stake 質押 → pending 隨區塊累積
//  5. POST /dex/stake/claim 領取（金庫鏈上轉帳回 admin）
//  6. 質押中全額退出被拒；解除後可退

func stakeSetup(t *testing.T) (*httptest.Server, string, string, string, string) {
	t.Helper()
	n, ts, _ := contractStudioSetup(t)
	// 沿用 dexSetup 的發幣流程（deploy TKA/TKB）。
	tkA := deployToken(t, ts, "TokenA", "TKA")
	tkB := deployToken(t, ts, "TokenB", "TKB")
	// 建池＋注入流動性（100000/50000 → LP=70710，admin 持倉）。
	r := studioPostJSON(t, ts, "/dex/create", map[string]any{
		"token0": tkA, "token1": tkB,
	})
	if r["ok"] != true {
		t.Fatalf("建池失敗: %v", r)
	}
	pool, _ := r["pool"].(map[string]any)
	pid := pool["id"].(string)
	r2 := studioPostJSON(t, ts, "/dex/liquidity/add", map[string]any{
		"pool": pid, "amount0": "100000", "amount1": "50000",
	})
	if r2["ok"] != true {
		t.Fatalf("注入流動性失敗: %v", r2)
	}
	// 建農場：獎勵代幣 TKA、每塊 1 顆（1e18 raw）。
	r3 := studioPostJSON(t, ts, "/dex/stake/create", map[string]any{
		"pool": pid, "reward_token": tkA, "reward_per_block": "1000000",
	})
	if r3["ok"] != true {
		t.Fatalf("建農場失敗: %v", r3)
	}
	// 注資 10 顆 TKA（1e19 raw）至金庫。
	r4 := studioPostJSON(t, ts, "/dex/stake/fund", map[string]any{
		"pool": pid, "amount": "100000000000",
	})
	if r4["ok"] != true {
		t.Fatalf("注資失敗: %v", r4)
	}
	return ts, tkA, tkB, pid, n.nodeAddress
}

// deployToken 部署標準代幣並等待入塊。
func deployToken(t *testing.T, ts *httptest.Server, name, symbol string) string {
	t.Helper()
	r := studioPostJSON(t, ts, "/contract/deploy", map[string]any{
		"name": name, "symbol": symbol, "supply": "1000000000000",
	})
	if r["ok"] != true {
		t.Fatalf("部署 %s 失敗: %v", symbol, r)
	}
	addr := r["contract_address"].(string)
	// 等待 VM 世界狀態同步（合約真正入塊）。
	waitFor(t, func() bool {
		l := studioGetJSON(t, ts, "/contract/list")
		infos, _ := l["contracts"].([]any)
		for _, it := range infos {
			m, _ := it.(map[string]any)
			if m["address"] == addr {
				cs, _ := m["code_size"].(float64)
				return cs > 0
			}
		}
		return false
	}, 15*time.Second, symbol+" 合約入塊")
	return addr
}

func TestDEXStakeCreateFundAndPending(t *testing.T) {
	ts, tkA, tkB, pid, _ := stakeSetup(t)
	_ = tkB
	// 農場列表。
	l := studioGetJSON(t, ts, "/dex/stake/pools")
	if l["ok"] != true {
		t.Fatalf("農場列表失敗: %v", l)
	}
	pools, _ := l["pools"].([]any)
	if len(pools) != 1 {
		t.Fatalf("應有 1 個農場，得 %d", len(pools))
	}
	sp, _ := pools[0].(map[string]any)
	if sp["pool_id"] != pid || sp["reward_token"] != tkA {
		t.Fatalf("農場欄位異常: %v", sp)
	}
	if sp["funded"] != "100000000000" {
		t.Fatalf("funded 應為 1e19，得 %v", sp["funded"])
	}
	// 重複建農場拒絕。
	r := studioPostJSON(t, ts, "/dex/stake/create", map[string]any{
		"pool": pid, "reward_token": tkA, "reward_per_block": "1",
	})
	if r["ok"] == true {
		t.Fatalf("重複建農場應拒絕")
	}
	// 質押 70710 份額。
	r2 := studioPostJSON(t, ts, "/dex/stake", map[string]any{"pool": pid, "shares": "70710"})
	if r2["ok"] != true {
		t.Fatalf("質押失敗: %v", r2)
	}
	// 質押後 pending 應 ≥ 0（隨區塊累積）。
	waitFor(t, func() bool {
		q := studioGetJSON(t, ts, "/dex/stake/pending?pool="+pid)
		p, _ := q["pending"].(string)
		bi, _ := new(big.Int).SetString(p, 10)
		return bi != nil && bi.Sign() > 0
	}, 20*time.Second, "pending 隨區塊累積")
}

func TestDEXStakeClaimAndLock(t *testing.T) {
	ts, tkA, _, pid, adminAddr := stakeSetup(t)
	// admin 質押前餘額。
	before := studioGetJSON(t, ts, "/contract/erc20/"+tkA+"?holder="+adminAddr)
	// 質押。
	r := studioPostJSON(t, ts, "/dex/stake", map[string]any{"pool": pid, "shares": "70710"})
	if r["ok"] != true {
		t.Fatalf("質押失敗: %v", r)
	}
	// 質押中全額退出被拒。
	r2 := studioPostJSON(t, ts, "/dex/liquidity/remove", map[string]any{"pool": pid, "shares": "70710"})
	if r2["ok"] == true {
		t.Fatalf("質押中全額退出應被拒")
	}
	// 等 pending 累積後領取。
	waitFor(t, func() bool {
		q := studioGetJSON(t, ts, "/dex/stake/pending?pool="+pid)
		p, _ := q["pending"].(string)
		bi, _ := new(big.Int).SetString(p, 10)
		return bi != nil && bi.Sign() > 0
	}, 20*time.Second, "pending 累積")
	q := studioGetJSON(t, ts, "/dex/stake/pending?pool="+pid)
	t.Logf("claim 前 pending=%v", q["pending"])
	cl := studioPostJSON(t, ts, "/dex/stake/claim", map[string]any{"pool": pid})
	if cl["ok"] != true {
		t.Fatalf("領取失敗: %v", cl)
	}
	amt, _ := new(big.Int).SetString(cl["amount"].(string), 10)
	if amt == nil || amt.Sign() <= 0 {
		t.Fatalf("領取數量異常: %v", cl)
	}
	// admin 獎勵代幣餘額增加。
	waitFor(t, func() bool {
		after := studioGetJSON(t, ts, "/contract/erc20/"+tkA+"?holder="+adminAddr)
		a, _ := after["balance"].(string)
		b, _ := before["balance"].(string)
		ba, _ := new(big.Int).SetString(a, 10)
		bb, _ := new(big.Int).SetString(b, 10)
		return ba != nil && bb != nil && ba.Cmp(bb) > 0
	}, 20*time.Second, "admin 獎勵代幣入帳")
	// 解除質押後可全額退出。
	r3 := studioPostJSON(t, ts, "/dex/stake/unstake", map[string]any{"pool": pid, "shares": "70710"})
	if r3["ok"] != true {
		t.Fatalf("解除質押失敗: %v", r3)
	}
	r4 := studioPostJSON(t, ts, "/dex/liquidity/remove", map[string]any{"pool": pid, "shares": "70710"})
	if r4["ok"] != true {
		t.Fatalf("解除後退出應成功: %v", r4)
	}
}
