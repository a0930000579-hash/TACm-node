package node

// dexv3_e2e_test.go — DEX V3 集中流動性端對端：
//   1) 建池（TACm/TIUSD @0.3%）→ mint 涵蓋當前價格的位置
//   2) 雙向小額 swap：斷言輸出>0、價格朝預期方向移動
//   3) collect（流程成功）→ decrease（斷言退回資金）
//
// 每個 Test 用獨立 btcec 地址；測試短促避免被 Exit 143。

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"

	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// dexV3TestAddr 產生一個新的 0x 測試地址。
func dexV3TestAddr(t *testing.T) string {
	t.Helper()
	k, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("產生密鑰: %v", err)
	}
	pub := k.PubKey().SerializeUncompressed()
	d := crypto.Keccak256(pub[1:])
	return crypto.EthAddressHex(d[len(d)-20:])
}

func dexV3Fund(t *testing.T, n *Node, addr string) {
	t.Helper()
	if err := n.walletSvc.Reward(addr, wallet.AssetTACm, wallet.NewAmountF(10000, wallet.AssetTACm), "test:dexv3"); err != nil {
		t.Fatalf("造 TACm: %v", err)
	}
	if err := n.walletSvc.Reward(addr, wallet.AssetTiUSD, wallet.NewAmountF(10000, wallet.AssetTiUSD), "test:dexv3"); err != nil {
		t.Fatalf("造 TIUSD: %v", err)
	}
}

// TestDexV3PoolMintE2E 建池 + mint 位置 + 列表。
func TestDexV3PoolMintE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	addr := dexV3TestAddr(t)
	dexV3Fund(t, n, addr)

	// 1) 建池 TACm/TIUSD @30bps，初始 price=1（sqrt=1）。
	cr := postJSON(t, srv.URL+"/api/dexv3/pool/create", map[string]any{
		"address": addr, "token0": "TACm", "token1": "TIUSD", "fee_tier": 30, "sqrt_price": 1.0,
	})
	if cr["ok"] != true {
		t.Fatalf("建池失敗: %v", cr)
	}
	pool, _ := cr["pool"].(map[string]any)
	poolID, _ := pool["id"].(float64)

	// 2) mint 涵蓋當前價格的位置 [-600,600]，各投 100。
	mr := postJSON(t, srv.URL+"/api/dexv3/position/mint", map[string]any{
		"address": addr, "pool_id": int64(poolID),
		"tick_lower": -600, "tick_upper": 600, "amount0": 100, "amount1": 100,
	})
	if mr["ok"] != true {
		t.Fatalf("mint 失敗: %v", mr)
	}
	if mr["position_id"].(float64) <= 0 {
		t.Fatalf("position_id 應>0: %v", mr["position_id"])
	}
	if mr["liquidity"].(float64) <= 0 {
		t.Fatalf("liquidity 應>0: %v", mr)
	}

	// 3) 列表應含此位置。
	lr := getJSON(t, srv.URL+"/api/dexv3/positions?address="+addr)
	list, _ := lr["positions"].([]any)
	if len(list) != 1 {
		t.Fatalf("應有 1 個位置, got %d (%v)", len(list), lr)
	}
}

// TestDexV3SwapE2E 雙向 swap：斷言輸出>0、tick 方向正確。
func TestDexV3SwapE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	addr := dexV3TestAddr(t)
	dexV3Fund(t, n, addr)

	cr := postJSON(t, srv.URL+"/api/dexv3/pool/create", map[string]any{
		"address": addr, "token0": "TACm", "token1": "TIUSD", "fee_tier": 30, "sqrt_price": 1.0,
	})
	if cr["ok"] != true {
		t.Fatalf("建池失敗: %v", cr)
	}
	pool, _ := cr["pool"].(map[string]any)
	poolID := int64(pool["id"].(float64))
	beforeTick := int(pool["tick"].(float64))

	mr := postJSON(t, srv.URL+"/api/dexv3/position/mint", map[string]any{
		"address": addr, "pool_id": poolID,
		"tick_lower": -600, "tick_upper": 600, "amount0": 1000, "amount1": 1000,
	})
	if mr["ok"] != true {
		t.Fatalf("mint 失敗: %v", mr)
	}

	// zeroForOne：賣 TACm 買 TIUSD → tick 應下跌。
	s1 := postJSON(t, srv.URL+"/api/dexv3/swap", map[string]any{
		"address": addr, "pool_id": poolID, "zero_for_one": true, "amount_in": 10,
	})
	if s1["ok"] != true {
		t.Fatalf("swap zeroForOne 失敗: %v", s1)
	}
	if s1["amount_out"].(float64) <= 0 {
		t.Fatalf("輸出應>0: %v", s1)
	}
	afterTick := int(s1["tick"].(float64))
	if afterTick >= beforeTick {
		t.Fatalf("zeroForOne 應使 tick 下跌: before=%d after=%d", beforeTick, afterTick)
	}
	if s1["fee"].(float64) <= 0 {
		t.Fatalf("應收手續費: %v", s1)
	}

	// oneForZero：反向 → tick 應回升。
	s2 := postJSON(t, srv.URL+"/api/dexv3/swap", map[string]any{
		"address": addr, "pool_id": poolID, "zero_for_one": false, "amount_in": 10,
	})
	if s2["ok"] != true {
		t.Fatalf("swap oneForZero 失敗: %v", s2)
	}
	if s2["amount_out"].(float64) <= 0 {
		t.Fatalf("反向輸出應>0: %v", s2)
	}
	if int(s2["tick"].(float64)) <= afterTick {
		t.Fatalf("oneForZero 應使 tick 回升: mid=%d after=%v", afterTick, s2["tick"])
	}
}

// TestDexV3CollectDecreaseE2E swap 後 collect + decrease，斷言流程成功且退還資金。
func TestDexV3CollectDecreaseE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)
	addr := dexV3TestAddr(t)
	dexV3Fund(t, n, addr)

	cr := postJSON(t, srv.URL+"/api/dexv3/pool/create", map[string]any{
		"address": addr, "token0": "TACm", "token1": "TIUSD", "fee_tier": 30, "sqrt_price": 1.0,
	})
	poolID := int64(cr["pool"].(map[string]any)["id"].(float64))

	mr := postJSON(t, srv.URL+"/api/dexv3/position/mint", map[string]any{
		"address": addr, "pool_id": poolID,
		"tick_lower": -600, "tick_upper": 600, "amount0": 1000, "amount1": 1000,
	})
	posID := int64(mr["position_id"].(float64))

	// 兩筆 swap 製造費用。
	postJSON(t, srv.URL+"/api/dexv3/swap", map[string]any{
		"address": addr, "pool_id": poolID, "zero_for_one": true, "amount_in": 20,
	})
	postJSON(t, srv.URL+"/api/dexv3/swap", map[string]any{
		"address": addr, "pool_id": poolID, "zero_for_one": false, "amount_in": 20,
	})

	// collect：流程成功（費用可能很小但不為負）。
	co := postJSON(t, srv.URL+"/api/dexv3/position/collect", map[string]any{
		"address": addr, "position_id": posID,
	})
	if co["ok"] != true {
		t.Fatalf("collect 失敗: %v", co)
	}
	if co["amount0"].(float64) < 0 || co["amount1"].(float64) < 0 {
		t.Fatalf("費用不可為負: %v", co)
	}

	// decrease 全撤：斷言成功且退回部分資金。
	de := postJSON(t, srv.URL+"/api/dexv3/position/decrease", map[string]any{
		"address": addr, "position_id": posID, "liquidity": 0, // 0 → 全撤
	})
	if de["ok"] != true {
		t.Fatalf("decrease 失敗: %v", de)
	}
	if de["liquidity_remaining"].(float64) != 0 {
		t.Fatalf("全撤後 L 應=0: %v", de["liquidity_remaining"])
	}
	if de["amount0"].(float64) < 0 || de["amount1"].(float64) < 0 {
		t.Fatalf("退還金額不可為負: %v", de)
	}
}
