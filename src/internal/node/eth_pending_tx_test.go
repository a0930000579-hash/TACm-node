package node

import (
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// TestEthGetPendingTxByHash：eth_getTransactionByHash 對已提交未打包交易回 pending 物件
// （blockHash/blockNumber/transactionIndex = null）；入塊後回鏈上物件（blockHash 非 null）。
func TestEthGetPendingTxByHash(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	fromHash := crypto.Hash160(key.PubKey().SerializeCompressed())
	from := crypto.Hash160ToAddress(fromHash)
	insertGenesisWithAlloc(t, n, from)
	if err := n.Wallet().Deposit(from, wallet.AssetTACm,
		wallet.Amount{Big: new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))},
		"eth-pending-test"); err != nil {
		t.Fatalf("Deposit 失敗: %v", err)
	}
	admin0x, _ := tx0To0x(admin)
	from0x, _ := tx0To0x(from)

	var lastHash string
	pendingChecked := false
	for i := int64(0); i < 3; i++ {
		raw := ethSignRawTx(t, key, i, 1, 100, 5_000_000_000_000_000_000, mustBytes20(t, admin0x), nil, crypto.EthChainID)
		out := ethRPC(t, srv, "eth_sendRawTransaction", "0x"+raw)
		if e, ok := out["error"]; ok {
			t.Fatalf("eth_sendRawTransaction error: %v", e)
		}
		txHash, _ := out["result"].(string)
		if txHash == "" {
			t.Fatal("無 tx hash")
		}
		lastHash = txHash

		// 立即查（出塊間隔 1s，提交後毫秒級查詢）：未打包應回 pending（blockHash=null）。
		got := ethResult(t, srv, "eth_getTransactionByHash", "0x"+txHash)
		m, _ := got.(map[string]any)
		if m == nil {
			t.Fatalf("查詢結果非物件: %v", got)
		}
		if m["blockHash"] == nil {
			pendingChecked = true
			if m["from"] != from0x {
				t.Fatalf("pending from=%v 期望 %v", m["from"], from0x)
			}
			if m["to"] != admin0x {
				t.Fatalf("pending to=%v 期望 %v", m["to"], admin0x)
			}
			if m["type"] != "0x0" {
				t.Fatalf("pending type=%v 期望 0x0", m["type"])
			}
			wantChain := "0x" + strconv.FormatInt(crypto.EthChainID, 16)
			if m["chainId"] != wantChain {
				t.Fatalf("pending chainId=%v 期望 %v", m["chainId"], wantChain)
			}
			if m["blockNumber"] != nil || m["transactionIndex"] != nil {
				t.Fatalf("pending blockNumber/transactionIndex 應為 null: %v", m)
			}
			t.Logf("pending 路徑驗證通過 tx=%s", txHash)
			break
		}
		t.Logf("第 %d 筆已在 1s 內入塊（無妨，重試下一筆截獲 pending）", i)
	}
	if !pendingChecked {
		t.Log("三筆均在 1s 內入塊，pending 路徑未能截獲（未失敗，僅記 log）")
	}

	// 最後一筆確認入塊後：回鏈上物件（blockHash 非 null）。
	waitFor(t, func() bool {
		tx, err := n.db.GetTransaction(lastHash)
		return err == nil && tx != nil && tx.Status == "confirmed"
	}, 20*time.Second, "eth 交易未入塊")
	got := ethResult(t, srv, "eth_getTransactionByHash", "0x"+lastHash)
	m, _ := got.(map[string]any)
	if m == nil {
		t.Fatalf("入塊後查詢結果非物件: %v", got)
	}
	if m["blockHash"] == nil {
		t.Fatalf("入塊後 blockHash 應非 null: %v", m)
	}
	if m["from"] != from0x {
		t.Fatalf("鏈上 from=%v 期望 %v", m["from"], from0x)
	}
}

// TestEthBlockTagSafeFinalizedAndFields：block tag safe/finalized 映射最新塊，
// 且區塊物件帶主流兼容欄位（withdrawals/blobGasUsed/excessBlobGas）。
func TestEthBlockTagSafeFinalizedAndFields(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)
	waitFor(t, func() bool {
		return n.db.GetTipHeight() >= 1
	}, 8*time.Second, "無新塊產出")

	for _, tag := range []string{"safe", "finalized", "latest", "pending"} {
		got := ethResult(t, srv, "eth_getBlockByNumber", tag, false)
		m, _ := got.(map[string]any)
		if m == nil {
			t.Fatalf("%s 回非物件: %v", tag, got)
		}
		if _, ok := m["withdrawals"]; !ok {
			t.Fatalf("%s 缺 withdrawals 欄位", tag)
		}
		if m["blobGasUsed"] != "0x0" {
			t.Fatalf("%s blobGasUsed=%v 期望 0x0", tag, m["blobGasUsed"])
		}
		if m["excessBlobGas"] != "0x0" {
			t.Fatalf("%s excessBlobGas=%v 期望 0x0", tag, m["excessBlobGas"])
		}
		if _, ok := m["number"]; !ok {
			t.Fatalf("%s 缺 number 欄位", tag)
		}
	}
}

// TestRejectContractSender：EIP-3607——合約地址不得作為交易簽名者；
// 正常 EOA 地址不受影響（eth 路徑與 SubmitTransactions 共用同一檢查）。
func TestRejectContractSender(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 部署合約（真實上鏈）。
	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "EIP3607", "symbol": "E7", "supply": "1000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	contract0x, _ := tx0To0x(contractAddr)
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊部署")

	if err := n.rejectContractSender(contractAddr); err == nil {
		t.Fatal("合約地址作為 sender 應被拒絕（EIP-3607）")
	}
	if err := n.rejectContractSender(admin); err != nil {
		t.Fatalf("正常 EOA 地址不應被拒: %v", err)
	}
}
