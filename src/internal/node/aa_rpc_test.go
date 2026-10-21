package node

// M154 AA（EIP-4337）RPC e2e：部署 counter 合約 → UserOperation 簽名 → 提交 → 打包 → 狀態查詢。

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/aa"
	"tacm/internal/crypto"
	"tacm/internal/vm"
)

func newAATestServer(t *testing.T) (*httptest.Server, *Node, *crypto.KeyPair, string) {
	t.Helper()
	n, err := New(testCfg(t), "aa1", 3)
	if err != nil {
		t.Fatalf("建節點失敗: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	insertGenesisWithAlloc(t, n, alice)
	n.Start()

	// 預算並部署 counter 合約（alice nonce0）。
	alice0x, _ := tx0To0x(alice)
	contract0x := vm.CreateAddress(alice0x, 0)
	contractTx0, _ := evmToTx0(contract0x)
	runtime := buildCounterRuntime()
	deployMemo := vmDeployPrefix + hex.EncodeToString(buildInitFor(runtime))
	deployTx := signedTxWithMemo(t, aliceKP, contractTx0, "0", "1", 0, deployMemo)
	deployHash, err := n.SubmitTransaction(deployTx)
	if err != nil {
		t.Fatalf("部署交易被拒: %v", err)
	}
	waitFor(t, func() bool {
		if got, _ := n.DB().GetTransaction(deployHash); got == nil {
			return false
		}
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize == len(runtime)
	}, 15*time.Second, "counter 合約未部署")

	// 掛載 AA（Bundler=節點金鑰位址）。
	nodeAddr, _ := n.keypair.Address()
	n.aaSvc = aa.New(nodeAddr)

	srv := httptest.NewServer(NewRPCServer(n).Handler())
	t.Cleanup(srv.Close)
	return srv, n, aliceKP, contractTx0
}

// aaSubmitJSON 構建並提交一筆 UserOperation（alice 簽名），回傳響應 map。
func aaSubmitJSON(t *testing.T, srv *httptest.Server, kp *crypto.KeyPair, target, callData string, nonce int64) map[string]any {
	t.Helper()
	alice, _ := kp.Address()
	op := &aa.UserOperation{
		Sender: alice, Nonce: nonce, Target: target, CallData: callData,
		CallGasLimit: 100000, PreVerificationGas: 21000,
		MaxFeePerGas: "0.000001", MaxPriorityFee: "0",
		Pubkey: hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	digest, err := aa.UserOpDigest(op)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.SignDigest(digest, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	op.Signature = hex.EncodeToString(sig)

	body, _ := json.Marshal(op)
	resp, err := srv.Client().Post(srv.URL+"/aa/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /aa/submit 失敗: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("回應解析失敗: %v", err)
	}
	return out
}

func TestAASubmitAndExecute(t *testing.T) {
	srv, n, aliceKP, contractTx0 := newAATestServer(t)

	// 提交 AA 操作：counter slot0 +1（callData "00" 觸發）。
	out := aaSubmitJSON(t, srv, aliceKP, contractTx0, "00", 0)
	if out["ok"] != true {
		t.Fatalf("AA 提交應成功，got %+v", out)
	}
	txHash, _ := out["tx_hash"].(string)
	if txHash == "" {
		t.Fatal("應回 tx_hash")
	}
	alice, _ := aliceKP.Address()
	alice0x, _ := tx0To0x(alice)
	contract0x := vm.CreateAddress(alice0x, 0)

	// 等待打包執行：slot0 應為 1（AA 代執行真實生效）。
	waitFor(t, func() bool {
		if got, _ := n.DB().GetTransaction(txHash); got == nil {
			return false
		}
		return n.contracts.StorageAt(contract0x, big.NewInt(0)).Cmp(big.NewInt(1)) == 0
	}, 20*time.Second, "AA 操作未打包或 slot 未更新")

	// 查詢 AA 狀態：應有 submitted 紀錄。
	st := aaGetStatus(t, srv, alice)
	ops, _ := st["operations"].([]any)
	if len(ops) == 0 {
		t.Fatal("AA 狀態應有操作紀錄")
	}
	first := ops[0].(map[string]any)
	if first["status"] != "submitted" || first["tx_hash"] != txHash {
		t.Fatalf("最新紀錄應為 submitted txHash，got %+v", first)
	}
}

func TestAASubmitRejectsBadSig(t *testing.T) {
	srv, _, aliceKP, contractTx0 := newAATestServer(t)

	alice, _ := aliceKP.Address()
	op := &aa.UserOperation{
		Sender: alice, Nonce: 0, Target: contractTx0, CallData: "00",
		CallGasLimit: 100000, PreVerificationGas: 21000,
		MaxFeePerGas: "0.000001", MaxPriorityFee: "0",
		Pubkey:      hex.EncodeToString(aliceKP.PublicKeyCompressed()),
		Signature:   strings.Repeat("00", 70),
	}
	body, _ := json.Marshal(op)
	resp, err := srv.Client().Post(srv.URL+"/aa/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("偽造簽名應 400，got %d", resp.StatusCode)
	}
}

func TestAAEstimateRPC(t *testing.T) {
	srv, _, aliceKP, contractTx0 := newAATestServer(t)
	alice, _ := aliceKP.Address()
	op := &aa.UserOperation{
		Sender: alice, Nonce: 0, Target: contractTx0, CallData: "00",
		CallGasLimit: 100000, PreVerificationGas: 21000,
		MaxFeePerGas: "0.000001", MaxPriorityFee: "0",
		Pubkey: hex.EncodeToString(aliceKP.PublicKeyCompressed()),
	}
	body, _ := json.Marshal(op)
	resp, err := srv.Client().Post(srv.URL+"/aa/estimate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /aa/estimate 失敗: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("估算應 ok，got %+v", out)
	}
	if gas, _ := out["gas"].(float64); gas <= 0 {
		t.Fatalf("gas 應為正值，got %+v", out)
	}
	if fee, _ := out["gas_fee"].(string); fee == "" {
		t.Fatalf("gas_fee 應為非空，got %+v", out)
	}
}

// aaGetStatus 查詢 sender 的 AA 操作紀錄。
func aaGetStatus(t *testing.T, srv *httptest.Server, sender string) map[string]any {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/aa/status?sender=" + sender)
	if err != nil {
		t.Fatalf("GET /aa/status 失敗: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("回應解析失敗: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("status 應 ok，got %+v", out)
	}
	return out
}
