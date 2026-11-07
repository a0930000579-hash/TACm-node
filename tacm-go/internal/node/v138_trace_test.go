package node

// v138：Parity replay 面（trace_transaction / trace_replayTransaction /
// trace_replayBlockTransactions）——模擬重放回 root frame＋traceAddress。

import (
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

// TestTraceReplay：部署 counter → 鏈上呼叫 → replay 三方法回根層 frame。
func TestTraceReplay(t *testing.T) {
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
	if _, err := n.SubmitTransaction(deployTx); err != nil {
		t.Fatalf("部署被拒: %v", err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize == len(runtime)
	}, 15*time.Second, "部署未完成")

	// 鏈上呼叫（fallback，nonce1）。
	callTx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", 1, "")
	callHash, err := n.SubmitTransaction(callTx)
	if err != nil {
		t.Fatalf("呼叫被拒: %v", err)
	}
	waitFor(t, func() bool {
		tx, _ := n.DB().GetTransaction(callHash)
		return tx != nil && tx.Status == "confirmed"
	}, 15*time.Second, "呼叫未確認")

	hashParam := "0x" + callHash

	// trace_transaction → 根層 frame（traceAddress=[]、type=call、from=alice）。
	res := ethRPC(t, srv, "trace_transaction", hashParam)
	if e, ok := res["error"]; ok {
		t.Fatalf("trace_transaction err: %v", e)
	}
	arr, ok := res["result"].([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("trace_transaction 應回 trace 陣列: %#v", res["result"])
	}
	root := arr[0].(map[string]any)
	if root["type"] != "call" {
		t.Fatalf("根層 type 應為 call: %#v", root)
	}
	ta, _ := root["traceAddress"].([]any)
	if len(ta) != 0 {
		t.Fatalf("根層 traceAddress 應為 []: %#v", root["traceAddress"])
	}
	act := root["action"].(map[string]any)
	if act["from"] != alice0x || act["to"] != contract0x {
		t.Fatalf("action from/to 不符: %#v", act)
	}

	// trace_replayTransaction → {output, stateDiff, trace}。
	res2 := ethRPC(t, srv, "trace_replayTransaction", hashParam)
	if e, ok := res2["error"]; ok {
		t.Fatalf("trace_replayTransaction err: %v", e)
	}
	m2, ok := res2["result"].(map[string]any)
	if !ok {
		t.Fatalf("replay 結果非 map: %#v", res2["result"])
	}
	if _, ok := m2["stateDiff"]; !ok {
		t.Fatalf("應含 stateDiff: %#v", m2)
	}
	if _, ok := m2["trace"]; !ok {
		t.Fatalf("應含 trace: %#v", m2)
	}

	// trace_replayBlockTransactions("latest") → 含部署+呼叫至少 1 筆。
	res3 := ethRPC(t, srv, "trace_replayBlockTransactions", "latest")
	if e, ok := res3["error"]; ok {
		t.Fatalf("replayBlock err: %v", e)
	}
	arr3, ok := res3["result"].([]any)
	if !ok || len(arr3) == 0 {
		t.Fatalf("replayBlock 應回陣列: %#v", res3["result"])
	}
}
