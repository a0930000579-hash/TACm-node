package node

// orderbook_e2e_test.go — 鏈上訂單簿 e2e：
//  A) 賣家掛單 → 買家吃單 → 驗雙邊餘額與手續費 → 撤單退還。
//  B) 市場列表、深度、成交流正確。

import (
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"

	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// obTestAddr 派生一組測試地址：回 (0x 地址, tx0 地址)。
func obTestAddr(t *testing.T) (string, string) {
	t.Helper()
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("生成密鑰: %v", err)
	}
	pub := key.PubKey().SerializeUncompressed()
	h := crypto.Keccak256(pub[1:])
	tail := h[12:]
	addr0x := crypto.EthAddressHex(tail)
	addrTx0 := crypto.Hash160ToAddress(tail)
	return addr0x, addrTx0
}

func obBalTACm(t *testing.T, n *Node, addr string) string {
	t.Helper()
	a, err := n.walletSvc.Balance(addr)
	if err != nil {
		t.Fatalf("查詢 TACm 餘額 %s: %v", addr, err)
	}
	return a.TACmBalance.String()
}

func obBalTiUSD(t *testing.T, n *Node, addr string) int64 {
	t.Helper()
	a, err := n.walletSvc.Balance(addr)
	if err != nil {
		t.Fatalf("查詢 TiUSD 餘額 %s: %v", addr, err)
	}
	return a.TiUSDBalance
}

func TestOrderBookE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	seller0x, sellerTx0 := obTestAddr(t)
	buyer0x, buyerTx0 := obTestAddr(t)

	// 造資金：賣家 10 TACm；買家 25 TiUSD。
	if err := n.walletSvc.Reward(sellerTx0, wallet.AssetTACm,
		wallet.NewAmountF(10, wallet.AssetTACm), "ob:fund-seller"); err != nil {
		t.Fatalf("造 TACm: %v", err)
	}
	if err := n.walletSvc.Reward(buyerTx0, wallet.AssetTiUSD,
		wallet.NewAmountF(25, wallet.AssetTiUSD), "ob:fund-buyer"); err != nil {
		t.Fatalf("造 TiUSD: %v", err)
	}

	// 1) 賣家掛賣單：1 TACm @ 2.5 TiUSD。
	sellRes := postJSON(t, srv.URL+"/api/orderbook/place", map[string]any{
		"address": seller0x, "market": "TACm/TiUSD", "side": "sell",
		"type": "limit", "price": 2.5, "amount": 1.0,
	})
	if sellRes["ok"] != true {
		t.Fatalf("賣家掛單失敗: %v", sellRes)
	}
	sellOrder := sellRes["order"].(map[string]interface{})
	if sellOrder["status"] != "open" {
		t.Fatalf("賣單應為 open, got %s", sellOrder["status"])
	}
	// 賣家 TACm 應為 9（鎖定 1 TACm 進 escrow，剩 9 在錢包）。
	if obBalTACm(t, n, sellerTx0) != "9000000000000000000" {
		t.Fatalf("賣家 TACm 應為 9 (鎖定 1), got %s", obBalTACm(t, n, sellerTx0))
	}

	// 2) 買家掛買單：0.5 TACm @ 2.5 TiUSD → 應立即成交。
	buyRes := postJSON(t, srv.URL+"/api/orderbook/place", map[string]any{
		"address": buyer0x, "market": "TACm/TiUSD", "side": "buy",
		"type": "limit", "price": 2.5, "amount": 0.5,
	})
	if buyRes["ok"] != true {
		t.Fatalf("買家掛單失敗: %v", buyRes)
	}
	buyOrder := buyRes["order"].(map[string]interface{})
	if buyOrder["status"] != "filled" {
		t.Fatalf("買單應為 filled, got %s", buyOrder["status"])
	}
	fills, _ := buyRes["fills"].([]interface{})
	if len(fills) != 1 {
		t.Fatalf("應有 1 筆成交, got %d", len(fills))
	}
	fill := fills[0].(map[string]interface{})
	if fill["price"].(float64) != 2.5 {
		t.Fatalf("成交價應 2.5, got %v", fill["price"])
	}
	if fill["amount"].(float64) != 0.5 {
		t.Fatalf("成交量應 0.5, got %v", fill["amount"])
	}

	// 3) 驗證餘額：
	//    賣家：TACm = 0.5（剩餘掛簿）；TiUSD = 1.25（成交收入）。
	//    買家：TACm = 0.5（買到的）；TiUSD = 25 - 1.25*1.001 = 23.74875。
	sellerTACm := obBalTACm(t, n, sellerTx0)
	sellerTiUSD := obBalTiUSD(t, n, sellerTx0)
	buyerTACm := obBalTACm(t, n, buyerTx0)
	buyerTiUSD := obBalTiUSD(t, n, buyerTx0)

	t.Logf("賣家 TACm=%s TiUSD=%d", sellerTACm, sellerTiUSD)
	t.Logf("買家 TACm=%s TiUSD=%d", buyerTACm, buyerTiUSD)

	// 賣家收到 1.25 TiUSD。
	if sellerTiUSD != 1250000 { // 1.25 * 1e6
		t.Fatalf("賣家 TiUSD 應 1.25, got %d", sellerTiUSD)
	}
	// 買家收到 0.5 TACm。
	if buyerTACm != "500000000000000000" { // 0.5 * 1e18
		t.Fatalf("買家 TACm 應 0.5, got %s", buyerTACm)
	}

	// 4) Fee 帳戶應收到 0.00125 TiUSD。
	feeBal := obBalTiUSD(t, n, "orderbook_fee")
	if feeBal != 1250 { // 0.00125 * 1e6
		t.Fatalf("fee 帳戶應 0.00125 TiUSD, got %d", feeBal)
	}

	// 5) 撤銷賣家剩餘掛單（0.5 TACm）。
	sellOrderID := int64(sellOrder["id"].(float64))
	cancelRes := postJSON(t, srv.URL+"/api/orderbook/cancel", map[string]any{
		"address": seller0x, "order_id": sellOrderID,
	})
	if cancelRes["ok"] != true {
		t.Fatalf("撤單失敗: %v", cancelRes)
	}
	// 賣家應收到 0.5 TACm 退還（9 + 0.5 = 9.5）。
	sellerTACmAfter := obBalTACm(t, n, sellerTx0)
	if sellerTACmAfter != "9500000000000000000" {
		t.Fatalf("撤單後賣家 TACm 應 9.5, got %s", sellerTACmAfter)
	}
}

func TestOrderBookMarketsDepthTrades(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	// 市場列表。
	markets := getJSON(t, srv.URL+"/api/orderbook/markets")
	if markets["ok"] != true {
		t.Fatalf("markets 失敗: %v", markets)
	}
	mlist, _ := markets["markets"].([]interface{})
	if len(mlist) != 1 {
		t.Fatalf("應有 1 個市場, got %d", len(mlist))
	}

	// 深度（空簿）。
	dep := getJSON(t, srv.URL+"/api/orderbook/depth?market=TACm/TiUSD")
	if dep["ok"] != true {
		t.Fatalf("depth 失敗: %v", dep)
	}
	d := dep["depth"].(map[string]interface{})
	bids, _ := d["bids"].([]interface{})
	asks, _ := d["asks"].([]interface{})
	if len(bids) != 0 || len(asks) != 0 {
		t.Fatalf("空簿應無深度, bids=%d asks=%d", len(bids), len(asks))
	}

	// 成交流（空）。
	tr := getJSON(t, srv.URL+"/api/orderbook/trades?market=TACm/TiUSD")
	if tr["ok"] != true {
		t.Fatalf("trades 失敗: %v", tr)
	}
	tlist, _ := tr["trades"].([]interface{})
	if len(tlist) != 0 {
		t.Fatalf("空簿應無成交, got %d", len(tlist))
	}
}
