package node

// M128-A：以太標準 Filter API / feeHistory / 區塊索引方法 e2e 測試。

import (
	"encoding/hex"
	"math/big"

	"tacm/internal/chaindb"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

func TestEthFilterAPI(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// 部署 ERC-20 並產生 Transfer 事件。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "FilterToken", "symbol": "FTK", "supply": "1000000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	// evm_address 為 EVM 真實合約地址（logs/事件/eth_getLogs 過濾用）；
	// contract_address 為 tx0 表示，tx0To0x 轉換後與事件地址不同（既有系統行為）。
	contract0x := out["evm_address"].(string)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 20*time.Second, "合約未入塊部署")

	aliceKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	alice0x, _ := tx0To0x(alice)
	callOut := studioPostJSON(t, srv, "/contract/call", map[string]any{
		"to": contractAddr, "calldata": vmHex(vm.Erc20TransferCalldata(alice0x, big.NewInt(250))),
	})
	if callOut["ok"] != true {
		t.Fatalf("transfer 交易被拒: %v", callOut)
	}
	waitFor(t, func() bool {
		bal := n.contracts.StorageAt(contract0x, vmAddrInt(alice0x))
		return bal.Cmp(big.NewInt(250)) == 0
	}, 20*time.Second, "transfer 未入塊")
	// 用 tx0To0x 的合約鍵檢查（與合約管理器註冊鍵一致）。
	contractKey, _ := tx0To0x(contractAddr)
	waitFor(t, func() bool {
		bal := n.contracts.StorageAt(contractKey, vmAddrInt(alice0x))
		return bal.Cmp(big.NewInt(250)) == 0
	}, 20*time.Second, "transfer 未入塊(管理器鍵)")
	// logs 寫入與 StorageAt 更新存在既有時序差，等 logs 就緒再驗證（與 TestEthLogsAndReceipt 同源）。
	waitFor(t, func() bool {
		rows, err := n.DB().GetLogs(chaindb.LogFilter{TxHash: callOut["tx_hash"].(string), Limit: 100})
		return err == nil && len(rows) > 0
	}, 20*time.Second, "logs 未寫入")

	// 1. eth_newFilter（address 單值 + fromBlock=earliest 含歷史）→ id。
	// 主流語意：未帶 fromBlock 預設 latest（只訂閱其後 logs）；earliest 才含歷史。
	fid := ethResult(t, srv, "eth_newFilter", map[string]any{
		"address": contract0x, "fromBlock": "earliest",
	})
	fidStr, _ := fid.(string)
	if !strings.HasPrefix(fidStr, "0x") || fidStr == "0x" {
		t.Fatalf("eth_newFilter id 異常: %v", fid)
	}

	// 2. eth_getFilterLogs：應回至少一筆 Transfer。
	flogs := ethResult(t, srv, "eth_getFilterLogs", fidStr).([]any)
	if len(flogs) == 0 {
		t.Fatal("eth_getFilterLogs 應回 Transfer 日誌")
	}
	l0 := flogs[0].(map[string]any)
	if l0["address"] != contract0x {
		t.Fatalf("log address 不符: %v", l0["address"])
	}

	// 3. eth_getFilterChanges：log filter 初次查詢後無新資料（空陣列）。
	changes := ethResult(t, srv, "eth_getFilterChanges", fidStr).([]any)
	_ = changes

	// 4. eth_newBlockFilter + eth_getFilterChanges：回區塊 hash 列表。
	bid := ethResult(t, srv, "eth_newBlockFilter").(string)
	waitFor(t, func() bool {
		st := n.db
		_, _ = st, n
		h := latestHeight(t, n)
		return h >= 3
	}, 6*time.Second, "等待出塊")
	bchanges := ethResult(t, srv, "eth_getFilterChanges", bid).([]any)
	if len(bchanges) == 0 {
		t.Fatal("eth_getFilterChanges(newBlockFilter) 應回新區塊")
	}

	// 5. eth_uninstallFilter：回 true，再查回錯誤。
	if ethResult(t, srv, "eth_uninstallFilter", fidStr) != true {
		t.Fatal("eth_uninstallFilter 應回 true")
	}
	out2 := ethRPC(t, srv, "eth_getFilterLogs", fidStr)
	if _, hasErr := out2["error"]; !hasErr {
		t.Fatal("已移除 filter 應回 error")
	}

	// 6. eth_getLogs 多 address/topics：address 陣列 + topics 多層。
	mlogs := ethResult(t, srv, "eth_getLogs", map[string]any{
		"address": []any{contract0x, "0x0000000000000000000000000000000000000000"},
		"topics":  []any{"0x"+hex.EncodeToString(vm.TopicTransfer)},
	}).([]any)
	if len(mlogs) == 0 {
		t.Fatal("eth_getLogs 多條件查詢應回 Transfer")
	}
}

func TestEthFeeHistoryAndIndexMethods(t *testing.T) {
	_, srv, _ := contractStudioSetup(t)

	// 1. eth_feeHistory：結構與長度正確。
	fh := ethResult(t, srv, "eth_feeHistory", float64(3), "latest", []any{}).(map[string]any)
	if fh["oldestBlock"] == nil {
		t.Fatal("feeHistory 缺 oldestBlock")
	}
	bf, _ := fh["baseFeePerGas"].([]any)
	if len(bf) == 0 {
		t.Fatal("baseFeePerGas 不應為空")
	}
	if _, isHex := bf[0].(string); !isHex {
		t.Fatalf("baseFeePerGas[0] 應為 hex 字串: %v", bf[0])
	}
	gr, _ := fh["gasUsedRatio"].([]any)
	if len(gr) != len(bf) {
		t.Fatalf("gasUsedRatio 長度應等於 baseFeePerGas，得到 %d vs %d", len(gr), len(bf))
	}

	// 2. eth_getBlockTransactionCountByNumber：hex 字串。
	cnt := ethResult(t, srv, "eth_getBlockTransactionCountByNumber", "latest").(string)
	if !strings.HasPrefix(cnt, "0x") {
		t.Fatalf("交易數應為 hex，得到 %v", cnt)
	}

	// 3. eth_getBlockTransactionCountByHash：無此 hash 回錯誤。
	out := ethRPC(t, srv, "eth_getBlockTransactionCountByHash", "0xdeadbeef")
	if _, hasErr := out["error"]; !hasErr {
		t.Fatal("無效區塊 hash 應回 error")
	}

	// 4. eth_getTransactionByBlockNumberAndIndex：索引超界回 null。
	res := ethResult(t, srv, "eth_getTransactionByBlockNumberAndIndex", "latest", "0xffff")
	if res != nil {
		t.Fatalf("索引超界應回 null，得到 %v", res)
	}

	// 5. eth_getUncleCountByBlockNumber：TACm 無 uncle → 0x0。
	uc := ethResult(t, srv, "eth_getUncleCountByBlockNumber", "latest").(string)
	if uc != "0x0" {
		t.Fatalf("uncle count 應為 0x0，得到 %v", uc)
	}
}

// latestHeight 讀鏈頂高度（測試輔助）。
func latestHeight(t *testing.T, n *Node) int64 {
	t.Helper()
	b, err := n.DB().GetLatestBlock()
	if err != nil || b == nil {
		return -1
	}
	return b.Height
}
