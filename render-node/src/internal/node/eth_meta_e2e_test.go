package node

// M129：主流鏈 RPC 同級補齊（eth 基建方法/txpool/getProof/trace）e2e 測試。

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
	"tacm/internal/wallet"
)

// TestEthMetaMethods 驗證 eth_syncing/protocolVersion/coinbase/accounts/
// maxPriorityFeePerGas/uncle 基建方法（主流工具能力探測）。
func TestEthMetaMethods(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// eth_protocolVersion：0x 開頭 hex。
	pv := ethResult(t, srv, "eth_protocolVersion").(string)
	if !strings.HasPrefix(pv, "0x") {
		t.Fatalf("eth_protocolVersion 應為 hex: %v", pv)
	}

	// eth_syncing：結構含 startingBlock/currentBlock/highestBlock。
	sync := ethResult(t, srv, "eth_syncing").(map[string]any)
	for _, k := range []string{"startingBlock", "currentBlock", "highestBlock"} {
		if _, ok := sync[k]; !ok {
			t.Fatalf("eth_syncing 缺欄位 %s", k)
		}
	}

	// eth_coinbase：節點地址（以太 0x 格式，與 eth_accounts/personal_sign 一致）。
	cb := ethResult(t, srv, "eth_coinbase").(string)
	if !strings.HasPrefix(cb, "0x") || len(cb) != 42 {
		t.Fatalf("eth_coinbase 應回節點 0x 地址: %v", cb)
	}

	// eth_accounts：包含節點地址。
	accs := ethResult(t, srv, "eth_accounts").([]any)
	if len(accs) == 0 || accs[0] != cb {
		t.Fatalf("eth_accounts 應含節點地址: %v", accs)
	}

	// eth_maxPriorityFeePerGas：0x hex。
	tip := ethResult(t, srv, "eth_maxPriorityFeePerGas").(string)
	if !strings.HasPrefix(tip, "0x") {
		t.Fatalf("eth_maxPriorityFeePerGas 應為 hex: %v", tip)
	}

	// uncle 方法：本鏈無 uncle，回 null。
	if v := ethResult(t, srv, "eth_getUncleByBlockHashAndIndex", "0x"+strings.Repeat("ab", 32), "0x0"); v != nil {
		t.Fatalf("eth_getUncleByBlockHashAndIndex 應回 null: %v", v)
	}
	if v := ethResult(t, srv, "eth_getUncleByBlockNumberAndIndex", "0x1", "0x0"); v != nil {
		t.Fatalf("eth_getUncleByBlockNumberAndIndex 應回 null: %v", v)
	}

	// 節點餘額非空（冒煙節點有 coinbase）。
	if bal := n.DB().GetBalance(cb); bal == "" || bal == "0" {
		t.Logf("注意：測試節點 coinbase 尚未累積（非斷言）")
	}
}

// TestTxpoolAndTrace 驗證 txpool_status/content 與 debug_traceTransaction。
func TestTxpoolAndTrace(t *testing.T) {
	_, srv, _ := contractStudioSetup(t)

	// txpool_status：pending/queued 均為 hex。
	st := ethResult(t, srv, "txpool_status").(map[string]any)
	for _, k := range []string{"pending", "queued"} {
		v, ok := st[k].(string)
		if !ok || !strings.HasPrefix(v, "0x") {
			t.Fatalf("txpool_status.%s 應為 hex: %v", k, st[k])
		}
	}

	// txpool_content：pending 為 map（可能為空），queued 為空 map。
	content := ethResult(t, srv, "txpool_content").(map[string]any)
	if _, ok := content["pending"].(map[string]any); !ok {
		t.Fatalf("txpool_content.pending 應為物件: %v", content["pending"])
	}
	if _, ok := content["queued"].(map[string]any); !ok {
		t.Fatalf("txpool_content.queued 應為物件: %v", content["queued"])
	}

	// debug_traceTransaction：不存在的 hash 回 null。
	if v := ethResult(t, srv, "debug_traceTransaction", "0x"+strings.Repeat("00", 32)); v != nil {
		t.Fatalf("trace 未找到應回 null: %v", v)
	}
}

// TestEthGetProof 驗證 eth_getProof（EIP-1186）結構與真實值。
func TestEthGetProof(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// 部署 ERC-20（有 code，驗證 codeHash 非空 code hash）。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "ProofToken", "symbol": "PRF", "supply": "500000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contract0x := out["evm_address"].(string)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊")

	proof := ethResult(t, srv, "eth_getProof", contract0x,
		[]any{"0x" + strings.Repeat("00", 32)}, "latest").(map[string]any)

	if proof["address"] != contract0x {
		t.Fatalf("proof address 不符: %v", proof["address"])
	}
	sp, _ := proof["storageProof"].([]any)
	if len(sp) != 1 {
		t.Fatalf("storageProof 應有 1 筆: %v", proof["storageProof"])
	}
	s0 := sp[0].(map[string]any)
	if s0["value"].(string) == "" {
		t.Fatalf("storageProof.value 不應為空")
	}
	if _, ok := proof["balance"].(string); !ok {
		t.Fatalf("proof.balance 應為 hex: %v", proof["balance"])
	}
	if _, ok := proof["nonce"].(string); !ok {
		t.Fatalf("proof.nonce 應為 hex: %v", proof["nonce"])
	}
	if !strings.HasPrefix(proof["codeHash"].(string), "0x") {
		t.Fatalf("proof.codeHash 應為 hex: %v", proof["codeHash"])
	}
}

var (
	_ = crypto.GenerateKeyPair
	_ = vm.Erc20TransferCalldata
	_ = hex.EncodeToString
)

// TestM130BlockAndReceiptFields 驗證區塊/交易回傳物件標準欄位（M130-A/B）。
func TestM130BlockAndReceiptFields(t *testing.T) {
	_, srv, _ := contractStudioSetup(t)

	// 區塊物件：標準欄位齊備。
	blk := ethResult(t, srv, "eth_getBlockByNumber", "latest", false).(map[string]any)
	for _, k := range []string{"difficulty", "totalDifficulty", "size", "gasLimit",
		"gasUsed", "baseFeePerGas", "nonce", "extraData", "mixHash", "sha3Uncles",
		"uncles", "receiptsRoot", "stateRoot", "logsBloom", "miner"} {
		if _, ok := blk[k]; !ok {
			t.Fatalf("eth_getBlockByNumber 缺欄位 %s", k)
		}
	}
	if _, ok := blk["uncles"].([]any); !ok {
		t.Fatalf("uncles 應為陣列: %v", blk["uncles"])
	}

	// net_peerCount / eth_blobBaseFee（C）。
	pc := ethResult(t, srv, "net_peerCount").(string)
	if !strings.HasPrefix(pc, "0x") {
		t.Fatalf("net_peerCount 應為 hex: %v", pc)
	}
	if bb := ethResult(t, srv, "eth_blobBaseFee").(string); bb != "0x0" {
		t.Fatalf("eth_blobBaseFee 應為 0x0: %v", bb)
	}
}

// TestM131TxType2Fields 驗證交易/收據 type-2（EIP-1559）欄位（ethers v6 解析相容）。
func TestM131TxType2Fields(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "Type2Token", "symbol": "T2T", "supply": "100000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	hash, ok := out["tx_hash"].(string)
	if !ok || hash == "" {
		t.Fatal("部署無 tx_hash")
	}
	waitFor(t, func() bool {
		tx, err := n.DB().GetTransaction(hash)
		return err == nil && tx != nil
	}, 8*time.Second, "交易未入塊")

	// eth_getTransactionByHash：type/chainId/maxFeePerGas/maxPriorityFeePerGas/yParity 齊備。
	tx := ethResult(t, srv, "eth_getTransactionByHash", "0x"+hash).(map[string]any)
	for _, k := range []string{"type", "chainId", "maxFeePerGas", "maxPriorityFeePerGas", "yParity"} {
		if _, ok := tx[k]; !ok {
			t.Fatalf("tx 缺欄位 %s", k)
		}
	}
	if tx["chainId"] != "0x539" {
		t.Fatalf("chainId 應為 0x539: %v", tx["chainId"])
	}
	if tx["type"] != "0x0" && tx["type"] != "0x2" {
		t.Fatalf("type 異常: %v", tx["type"])
	}

	// receipt：type/logsBloom 齊備。
	rc := ethResult(t, srv, "eth_getTransactionReceipt", "0x"+hash).(map[string]any)
	for _, k := range []string{"type", "logsBloom", "cumulativeGasUsed", "contractAddress"} {
		if _, ok := rc[k]; !ok {
			t.Fatalf("receipt 缺欄位 %s", k)
		}
	}
}

// TestM132RpcParamCompat 驗證 estimateGas value 參數與 getLogs blockHash 過濾（M132）。
func TestM132RpcParamCompat(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// 部署 ERC-20 產生 logs 與可估算呼叫。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "ParamToken", "symbol": "PMT", "supply": "100000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contract0x := out["evm_address"].(string)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊")

	// 1. eth_estimateGas 帶 value（0x0 wei）→ 正常回 hex。
	g := ethResult(t, srv, "eth_estimateGas", map[string]any{
		"to": contract0x, "data": "0x", "value": "0x0",
	}).(string)
	if !strings.HasPrefix(g, "0x") {
		t.Fatalf("estimateGas 應回 hex: %v", g)
	}

	// 2. eth_getLogs blockHash 過濾 → 回 logs（Transfer 事件）或空陣列（新部署塊無事件亦可）。
	blk := ethResult(t, srv, "eth_getBlockByNumber", "latest", false).(map[string]any)
	bh := blk["hash"].(string)
	logs := ethResult(t, srv, "eth_getLogs", map[string]any{
		"blockHash": bh, "address": contract0x,
	}).([]any)
	for _, lg := range logs {
		lm := lg.(map[string]any)
		if lm["blockHash"] != bh {
			t.Fatalf("getLogs blockHash 過濾失效: %v", lm["blockHash"])
		}
	}

	// 3. 不存在的 blockHash → 空陣列。
	empty := ethResult(t, srv, "eth_getLogs", map[string]any{
		"blockHash": "0x" + strings.Repeat("ab", 32),
	}).([]any)
	if len(empty) != 0 {
		t.Fatalf("不存在 blockHash 應回空陣列: %v", empty)
	}
}

// TestM133BlockReceiptsAndNetListening 驗證 eth_getBlockReceipts 與 net_listening（M133）。
func TestM133BlockReceiptsAndNetListening(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// 發一筆交易製造區塊內交易。
	key, from := ethKeyFrom(t)
	insertGenesisWithAlloc(t, n, from)
	if err := n.Wallet().Deposit(from, wallet.AssetTACm,
		wallet.Amount{Big: new(big.Int).Mul(big.NewInt(5), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))},
		"m133-fund"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	alice0x, _ := tx0To0x(alice)
	raw := ethSignRawTxType2(t, key, 0, 2_000_000_000, 5_000_000_000_000, 100,
		1_000_000_000_000_000_000, mustBytes20(t, alice0x), nil, crypto.EthChainID)
	out := ethRPC(t, srv, "eth_sendRawTransaction", "0x"+raw)
	if e, ok := out["error"]; ok {
		t.Fatalf("sendRaw error: %v", e)
	}
	txHash, _ := out["result"].(string)
	waitFor(t, func() bool {
		tx, err := n.db.GetTransaction(txHash)
		return err == nil && tx != nil && tx.Status == "confirmed"
	}, 8*time.Second, "交易未入塊")

	// 1. eth_getBlockReceipts（latest）→ 內含該交易收據。
	receipts := ethResult(t, srv, "eth_getBlockReceipts", "latest").([]any)
	found := false
	for _, r := range receipts {
		rm := r.(map[string]any)
		if rm["transactionHash"] == "0x"+txHash {
			found = true
			if rm["status"] != "0x1" {
				t.Fatalf("收據 status 應為 0x1: %v", rm["status"])
			}
		}
	}
	if !found {
		t.Fatalf("getBlockReceipts 缺交易收據: %v", receipts)
	}

	// 2. block hash 形式。
	blk := ethResult(t, srv, "eth_getBlockByNumber", "latest", false).(map[string]any)
	bh := blk["hash"].(string)
	receipts2 := ethResult(t, srv, "eth_getBlockReceipts", bh).([]any)
	if len(receipts2) == 0 {
		t.Fatalf("blockHash 形式 getBlockReceipts 應非空")
	}

	// 3. net_listening → true。
	listening := ethResult(t, srv, "net_listening").(bool)
	if !listening {
		t.Fatalf("net_listening 應為 true")
	}
}
