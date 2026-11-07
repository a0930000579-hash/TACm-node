package node

// v130：鏈上拍賣（Auction）單元測試——建立/競價/結算/流拍/查詢。

import (
	"path/filepath"
	"testing"
)

func newTestAuction(t *testing.T) *AuctionManager {
	t.Helper()
	m, err := NewAuctionManager(filepath.Join(t.TempDir(), "auction.db"))
	if err != nil {
		t.Fatalf("NewAuctionManager: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestAuctionCreateValidate(t *testing.T) {
	m := newTestAuction(t)
	// 賣方地址無效
	if _, err := m.CreateAuction("bad", "item", "TACm", "1000", 20, 10, ""); err == nil {
		t.Fatal("expected error for invalid seller")
	}
	// 空品項
	if _, err := m.CreateAuction("tx0000000000000000000001", "", "TACm", "1000", 20, 10, ""); err == nil {
		t.Fatal("expected error for empty item")
	}
	// 非法資產
	if _, err := m.CreateAuction("tx0000000000000000000001", "item", "XYZ", "1000", 20, 10, ""); err == nil {
		t.Fatal("expected error for invalid asset")
	}
	// 結束高度 ≤ 目前高度
	if _, err := m.CreateAuction("tx0000000000000000000001", "item", "TACm", "1000", 10, 10, ""); err == nil {
		t.Fatal("expected error for end_height <= current")
	}
	// 非法金額
	if _, err := m.CreateAuction("tx0000000000000000000001", "item", "TACm", "abc", 20, 10, ""); err == nil {
		t.Fatal("expected error for bad start price")
	}
	// 正常建立
	id, err := m.CreateAuction("tx0000000000000000000001", "item", "TACm", "1000", 20, 10, "h1")
	if err != nil || id != 1 {
		t.Fatalf("CreateAuction: id=%d err=%v", id, err)
	}
}

func TestAuctionBidAndSettle(t *testing.T) {
	m := newTestAuction(t)
	seller := "tx0000000000000000000001"
	alice := "tx0000000000000000000002"
	bob := "tx0000000000000000000003"
	id, err := m.CreateAuction(seller, "nft-001", "TACm", "1000", 20, 10, "h1")
	if err != nil {
		t.Fatalf("CreateAuction: %v", err)
	}
	// 低於起標價
	if err := m.Bid(id, alice, "500", 12, ""); err == nil {
		t.Fatal("expected error for bid below start price")
	}
	// 正常出價
	if err := m.Bid(id, alice, "1500", 12, "h2"); err != nil {
		t.Fatalf("Bid alice: %v", err)
	}
	// 平價出價被拒（需嚴格高於）
	if err := m.Bid(id, bob, "1500", 13, "h3"); err == nil {
		t.Fatal("expected error for equal bid")
	}
	// 更高出價
	if err := m.Bid(id, bob, "2000", 13, "h4"); err != nil {
		t.Fatalf("Bid bob: %v", err)
	}
	// 未到期結算被拒
	if _, err := m.Settle(id, 19); err == nil {
		t.Fatal("expected error settling before end")
	}
	// 到期結算：bob（2000 TACm）得標（金額以最小單位 wei 存）。
	auc, err := m.Settle(id, 20)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if auc.Status != auctionSold || auc.Winner != bob || auc.FinalPrice != "2000000000000000000000" {
		t.Fatalf("unexpected settle result: %+v", auc)
	}
	// 已結算不可再出價
	if err := m.Bid(id, alice, "3000", 21, ""); err == nil {
		t.Fatal("expected error bidding after settle")
	}
	// 出價清單
	bids, err := m.Bids(id)
	if err != nil || len(bids) != 2 {
		t.Fatalf("Bids: %v %v", bids, err)
	}
	if bids[0].Bidder != bob {
		t.Fatalf("top bid should be bob: %+v", bids)
	}
}

func TestAuctionUnsold(t *testing.T) {
	m := newTestAuction(t)
	id, err := m.CreateAuction("tx0000000000000000000001", "rare-item", "TACm", "1000", 15, 10, "")
	if err != nil {
		t.Fatalf("CreateAuction: %v", err)
	}
	auc, err := m.Settle(id, 16)
	if err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if auc.Status != auctionUnsold || auc.Winner != "" {
		t.Fatalf("expected unsold: %+v", auc)
	}
}

func TestAuctionListAndInfo(t *testing.T) {
	m := newTestAuction(t)
	_, _ = m.CreateAuction("tx0000000000000000000001", "a", "TACm", "100", 20, 10, "")
	_, _ = m.CreateAuction("tx0000000000000000000001", "b", "TiUSD", "50", 20, 10, "")
	all, err := m.Auctions("")
	if err != nil || len(all) != 2 {
		t.Fatalf("Auctions: %v %v", all, err)
	}
	open, err := m.Auctions(auctionOpen)
	if err != nil || len(open) != 2 {
		t.Fatalf("open auctions want 2 got %d (%v)", len(open), err)
	}
	info, err := m.AuctionInfo(1)
	if err != nil || info == nil || info.Item != "a" {
		t.Fatalf("AuctionInfo: %+v %v", info, err)
	}
	if _, err := m.AuctionInfo(99); err != nil {
		t.Fatalf("AuctionInfo missing should return nil,nil: %v", err)
	}
}
