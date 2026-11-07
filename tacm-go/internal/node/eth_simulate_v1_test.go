package node

// v151-eth_simulateV1：主流 eth_simulateV1 規格相容面測試。
// 驗證：空 calls 回傳結構、無參數錯誤、單筆 call returnData 項目。

import (
	"testing"
)

func TestEthSimulateV1(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	// 1) 空 calls → 一組結果、空 returnData/logs、含 stateDiff。
	out := ethRPC(t, srv, "eth_simulateV1", []any{map[string]any{"calls": []any{}}})
	if e := out["error"]; e != nil {
		t.Fatalf("空 calls 不應錯誤: %v", e)
	}
	res, ok := out["result"].([]any)
	if !ok || len(res) != 1 {
		t.Fatalf("應回 1 組結果: %v", out["result"])
	}
	first, ok := res[0].(map[string]any)
	if !ok {
		t.Fatalf("結果項結構異常: %v", res[0])
	}
	if _, has := first["stateDiff"]; !has {
		t.Fatalf("缺 stateDiff 欄位: %v", first)
	}
	if rd, ok := first["returnData"].([]any); !ok || len(rd) != 0 {
		t.Fatalf("空 calls 應無 returnData: %v", first["returnData"])
	}

	// 2) 無參數 → RPC 錯誤。
	if out = ethRPC(t, srv, "eth_simulateV1"); out["error"] == nil {
		t.Fatal("無參數應報錯")
	}

	// 3) 單筆 call（無 code 地址）→ returnData 項存在（"0x"）。
	out = ethRPC(t, srv, "eth_simulateV1", []any{map[string]any{
		"calls": []any{map[string]any{"to": "0x0000000000000000000000000000000000000001", "data": "0x"}},
	}})
	if e := out["error"]; e != nil {
		t.Fatalf("單筆 call 不應錯誤: %v", e)
	}
	res, ok = out["result"].([]any)
	if !ok || len(res) != 1 {
		t.Fatalf("應回 1 組結果: %v", out["result"])
	}
	first, ok = res[0].(map[string]any)
	if !ok {
		t.Fatalf("結果項異常: %v", res[0])
	}
	rd, _ := first["returnData"].([]any)
	if len(rd) != 1 {
		t.Fatalf("應有 1 筆 returnData: %v", first["returnData"])
	}
}
