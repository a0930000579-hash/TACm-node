package node

// v139：trace_callMany（批次模擬）＋ trace_rawTransaction（未上鏈重放）。

import (
	"math/big"
	"net/http/httptest"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// TestTraceCallManyRaw：部署 counter → trace_callMany 回陣列；raw 交易重放回 {output,stateDiff,trace}。
func TestTraceCallManyRaw(t *testing.T) {
	n, err := New(testCfg(t), "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	fromHash := crypto.Hash160(key.PubKey().SerializeCompressed())
	alice := crypto.Hash160ToAddress(fromHash)
	insertGenesisWithAlloc(t, n, alice)
	if err := n.Wallet().Deposit(alice, wallet.AssetTACm,
		wallet.Amount{Big: new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))},
		"v139-test"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	n.Start()
	t.Cleanup(func() { _ = n.Close() })
	srv := httptest.NewServer(NewRPCServer(n).mux)
	t.Cleanup(srv.Close)

	admin0x, _ := tx0To0x(alice)

	// trace_callMany：單筆 call object（轉帳到 alice 自身）。
	res := ethRPC(t, srv, "trace_callMany",
		[]any{map[string]any{"from": admin0x, "to": admin0x, "value": "0x1"}},
		[]any{"trace"}, "latest")
	if e, ok := res["error"]; ok {
		t.Fatalf("trace_callMany err: %v", e)
	}
	arr, ok := res["result"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("trace_callMany 應回 1 筆: %#v", res["result"])
	}
	m0 := arr[0].(map[string]any)
	if _, ok := m0["gasUsed"]; !ok {
		t.Fatalf("callMany 摘要缺 gasUsed: %#v", m0)
	}

	// trace_rawTransaction：EIP-1559 raw（轉 1 wei 到 admin0x；不入池）。
	raw := ethSignRawTx(t, key, 0, 2, 21000, 5_000_000_000_000_000_000, mustBytes20(t, admin0x), nil, crypto.EthChainID)
	res2 := ethRPC(t, srv, "trace_rawTransaction", "0x"+raw)
	if e, ok := res2["error"]; ok {
		t.Fatalf("trace_rawTransaction err: %v", e)
	}
	m2, ok := res2["result"].(map[string]any)
	if !ok {
		t.Fatalf("raw 重放非 map: %#v", res2["result"])
	}
	if _, ok := m2["stateDiff"]; !ok {
		t.Fatalf("raw 重放缺 stateDiff: %#v", m2)
	}
	traceArr, ok := m2["trace"].([]any)
	if !ok || len(traceArr) == 0 {
		t.Fatalf("raw 重放缺 trace 樹: %#v", m2)
	}
	root := traceArr[0].(map[string]any)
	if root["type"] != "call" {
		t.Fatalf("raw 根層 type 應 call: %#v", root)
	}
}
