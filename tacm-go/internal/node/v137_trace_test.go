package node

// v137：trace_call / debug_traceCall 回真實逐 opcode structLogs（VM 步驟收集）。

import (
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// TestTraceCallStructLogs：部署 counter → trace_call / debug_traceCall 回非空 structLogs。
func TestTraceCallStructLogs(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	insertGenesisWithAlloc(t, n, alice)
	n.Start()
	t.Cleanup(func() { _ = n.Close() })
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(srv.Close)

	alice0x, _ := tx0To0x(alice)
	contract0x := vm.CreateAddress(alice0x, 0)
	contractTx0, _ := evmToTx0(contract0x)

	runtime := buildCounterRuntime()
	initCode := buildInitFor(runtime)
	deployMemo := vmDeployPrefix + hex.EncodeToString(initCode)
	deployTx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", 0, deployMemo)
	deployHash, err := n.SubmitTransaction(deployTx)
	if err != nil {
		t.Fatalf("部署被拒: %v", err)
	}
	waitFor(t, func() bool {
		if got, _ := n.DB().GetTransaction(deployHash); got == nil {
			return false
		}
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize == len(runtime)
	}, 15*time.Second, "部署未完成")

	callObj := map[string]any{"to": contract0x, "from": alice0x, "data": "0x", "value": "0x0"}

	// debug_traceCall：structLogs 非空且含 SLOAD opcode。
	res := ethRPC(t, srv, "debug_traceCall", callObj)
	if e, ok := res["error"]; ok {
		t.Fatalf("debug_traceCall err: %v", e)
	}
	m, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("result 非 map: %#v", res["result"])
	}
	sl, ok := m["structLogs"].([]any)
	if !ok || len(sl) == 0 {
		t.Fatalf("structLogs 應非空: %#v", m["structLogs"])
	}
	first := sl[0].(map[string]any)
	if _, hasPC := first["pc"]; !hasPC {
		t.Fatalf("structLogs 首步缺 pc: %#v", first)
	}
	if _, hasGC := first["gasCost"]; !hasGC {
		t.Fatalf("首步缺 gasCost: %#v", first)
	}

	// trace_call 別名同參數 → 同結構。
	res2 := ethRPC(t, srv, "trace_call", callObj)
	if e, ok := res2["error"]; ok {
		t.Fatalf("trace_call err: %v", e)
	}
	m2, ok := res2["result"].(map[string]any)
	if !ok {
		t.Fatalf("trace_call result 非 map: %#v", res2["result"])
	}
	sl2, ok := m2["structLogs"].([]any)
	if !ok || len(sl2) == 0 {
		t.Fatalf("trace_call structLogs 應非空: %#v", m2["structLogs"])
	}
	// 以 op 欄位檢查至少含 SLOAD（0x54）——counter runtime 首步 SLOAD。
	found := false
	for _, st := range sl2 {
		if stm, ok2 := st.(map[string]any); ok2 && stm["op"] == "0x54" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("structLogs 應含 SLOAD(0x54): %#v", sl2)
	}
}
