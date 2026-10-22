package node

// aimarket_e2e_test.go — 鏈上 AI 模型市場 e2e：
//  A) per_call：owner 註冊 → list 含該模型 → caller invoke（charged≈10）→
//     模型 invoke_count+1 → owner 收到 ownerPart(9)、aimarket_fee 收到 protocolFee(1)。
//  B) per_period：owner 註冊 → caller subscribe 付費 → invoke 不再重複收 per-call 費 →
//     rate 5 分 → 模型平均分=5。

import (
	"math/big"
	"strconv"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"

	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// aimTestAddr 派生一組測試地址：回 (0x 地址, tx0 地址)。
// handler 會把 0x 正規化成 tx0，故造資金／查餘額都用 tx0 形式。
func aimTestAddr(t *testing.T) (string, string) {
	t.Helper()
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("生成密鑰: %v", err)
	}
	pub := key.PubKey().SerializeUncompressed() // 65 bytes，0x04 前綴
	h := crypto.Keccak256(pub[1:])
	tail := h[12:] // 末 20 bytes
	addr0x := crypto.EthAddressHex(tail)
	addrTx0 := crypto.Hash160ToAddress(tail)
	return addr0x, addrTx0
}

func aimTacmBal(t *testing.T, n *Node, addr string) *big.Int {
	t.Helper()
	a, err := n.walletSvc.Balance(addr)
	if err != nil {
		t.Fatalf("查詢餘額 %s: %v", addr, err)
	}
	return a.TACmBalance
}

func TestAIMarketPerCallE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	owner0x, ownerTx0 := aimTestAddr(t)
	caller0x, callerTx0 := aimTestAddr(t)

	// 給 caller 造 100000 TACm。
	if err := n.walletSvc.Reward(callerTx0, wallet.AssetTACm,
		wallet.NewAmountF(100000, wallet.AssetTACm), "aimarket:fund"); err != nil {
		t.Fatalf("造資金: %v", err)
	}

	// 1) owner 註冊 per_call 模型 price=10 TACm。
	reg := postJSON(t, srv.URL+"/api/aimarket/register", map[string]any{
		"address": owner0x, "name": "demo-chat", "metadata": "demo chat model",
		"cid": "QmDemo", "price": 10, "price_type": "per_call",
	})
	if reg["ok"] != true {
		t.Fatalf("註冊失敗: %v", reg)
	}
	m := reg["model"].(map[string]interface{})
	modelID := int64(m["id"].(float64))
	if m["price"].(float64) != 10 || m["price_type"] != "per_call" {
		t.Fatalf("模型價格/計費方式錯誤: %+v", m)
	}

	// 2) list 應含該模型。
	lst := getJSON(t, srv.URL+"/api/aimarket/list")
	models, _ := lst["models"].([]interface{})
	found := false
	for _, mm := range models {
		row := mm.(map[string]interface{})
		if int64(row["id"].(float64)) == modelID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list 未含新模型: %+v", lst)
	}

	// 3) caller invoke（input=hello）。
	inv := postJSON(t, srv.URL+"/api/aimarket/invoke", map[string]any{
		"address": caller0x, "model_id": modelID, "input": "hello",
	})
	if inv["ok"] != true {
		t.Fatalf("invoke 失敗: %v", inv)
	}
	res := inv["result"].(map[string]interface{})
	if res["invocation_id"].(float64) <= 0 {
		t.Fatalf("invocationId 應為正: %+v", res)
	}
	if res["charged"].(float64) != 10 {
		t.Fatalf("charged 應 10, got %v", res["charged"])
	}
	if res["source"] != "stub" {
		t.Fatalf("無 URL 應回 stub, got %v", res["source"])
	}

	// 4) 模型 invoke_count=1。
	got := getJSON(t, srv.URL+"/api/aimarket/model?id="+strconv.FormatInt(modelID, 10))
	gotM := got["model"].(map[string]interface{})
	if gotM["invoke_count"].(float64) != 1 {
		t.Fatalf("invoke_count 應 1, got %v", gotM["invoke_count"])
	}

	// 5) owner 收到 ownerPart=9；aimarket_fee 收到 protocolFee=1。
	if aimTacmBal(t, n, ownerTx0).Cmp(wallet.NewAmountF(9, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("owner 餘額應 9 TACm, got %s", aimTacmBal(t, n, ownerTx0).String())
	}
	if aimTacmBal(t, n, "aimarket_fee").Cmp(wallet.NewAmountF(1, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("aimarket_fee 餘額應 1 TACm, got %s", aimTacmBal(t, n, "aimarket_fee").String())
	}
}

func TestAIMarketPerPeriodE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	owner0x, ownerTx0 := aimTestAddr(t)
	caller0x, callerTx0 := aimTestAddr(t)
	if err := n.walletSvc.Reward(callerTx0, wallet.AssetTACm,
		wallet.NewAmountF(100000, wallet.AssetTACm), "aimarket:fund"); err != nil {
		t.Fatalf("造資金: %v", err)
	}

	// 1) owner 註冊 per_period 模型 price=100 TACm（期長預設 30 天）。
	reg := postJSON(t, srv.URL+"/api/aimarket/register", map[string]any{
		"address": owner0x, "name": "big-model", "metadata": "period model",
		"cid": "QmPeriod", "url": "", "price": 100, "price_type": "per_period",
	})
	if reg["ok"] != true {
		t.Fatalf("註冊失敗: %v", reg)
	}
	modelID := int64(reg["model"].(map[string]interface{})["id"].(float64))

	// 2) 未訂閱直接 invoke → 403。
	inv0 := postJSON(t, srv.URL+"/api/aimarket/invoke", map[string]any{
		"address": caller0x, "model_id": modelID, "input": "x",
	})
	if inv0["ok"] == true {
		t.Fatalf("未訂閱 invoke 應失敗: %v", inv0)
	}

	// 3) subscribe 付費 100 → owner 得 90、fee 得 10。
	sub := postJSON(t, srv.URL+"/api/aimarket/subscribe", map[string]any{
		"address": caller0x, "model_id": modelID,
	})
	if sub["ok"] != true {
		t.Fatalf("subscribe 失敗: %v", sub)
	}
	if aimTacmBal(t, n, ownerTx0).Cmp(wallet.NewAmountF(90, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("owner 訂閱後應 90, got %s", aimTacmBal(t, n, ownerTx0).String())
	}
	if aimTacmBal(t, n, "aimarket_fee").Cmp(wallet.NewAmountF(10, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("aimarket_fee 訂閱後應 10, got %s", aimTacmBal(t, n, "aimarket_fee").String())
	}

	// 4) 訂閱期內 invoke → charged=0（不再收 per-call）。
	inv1 := postJSON(t, srv.URL+"/api/aimarket/invoke", map[string]any{
		"address": caller0x, "model_id": modelID, "input": "hello-again",
	})
	if inv1["ok"] != true {
		t.Fatalf("訂閱後 invoke 失敗: %v", inv1)
	}
	if inv1["result"].(map[string]interface{})["charged"].(float64) != 0 {
		t.Fatalf("訂閱期內 charged 應 0, got %v", inv1["result"].(map[string]interface{})["charged"])
	}
	// owner 餘額仍為 90（未再增加）。
	if aimTacmBal(t, n, ownerTx0).Cmp(wallet.NewAmountF(90, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("訂閱期內 invoke 不應再付費, owner=%s", aimTacmBal(t, n, ownerTx0).String())
	}

	// 5) caller rate 5 分 → 模型平均分=5。
	rate := postJSON(t, srv.URL+"/api/aimarket/rate", map[string]any{
		"address": caller0x, "model_id": modelID, "score": 5, "comment": "great",
	})
	if rate["ok"] != true {
		t.Fatalf("rate 失敗: %v", rate)
	}
	gotM := getJSON(t, srv.URL+"/api/aimarket/model?id="+strconv.FormatInt(modelID, 10))["model"].(map[string]interface{})
	if gotM["avg_rating"].(float64) != 5 || gotM["rating_count"].(float64) != 1 {
		t.Fatalf("評分應 avg=5 count=1, got %+v", gotM)
	}

	// 6) 我的訂閱列表含此筆。
	subs := getJSON(t, srv.URL+"/api/aimarket/subscriptions?subscriber="+caller0x)
	if subs["ok"] != true {
		t.Fatalf("subscriptions 查詢失敗: %v", subs)
	}
	arr, _ := subs["subscriptions"].([]interface{})
	if len(arr) != 1 {
		t.Fatalf("應有 1 筆訂閱, got %d", len(arr))
	}
	if arr[0].(map[string]interface{})["active"] != true {
		t.Fatalf("訂閱應有效: %+v", arr[0])
	}
}
