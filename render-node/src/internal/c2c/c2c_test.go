package c2c

import (
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // 註冊 sqlite driver
)

func TestCreateAndListAds(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	id, err := st.CreateAd("tx0S", "sell", "USDT", "TWD", 31.5, 5, 100, []string{"銀行轉帳"})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if id <= 0 {
		t.Fatalf("ad_id 應 >0")
	}
	if _, err := st.CreateAd("tx0S", "sell", "ETH", "TWD", 31.5, 5, 100, []string{"銀行轉帳"}); err == nil {
		t.Fatalf("非法資產應拒")
	}
	if _, err := st.CreateAd("tx0S", "sell", "USDT", "TWD", 31.5, 5, 1, []string{"銀行轉帳"}); err == nil {
		t.Fatalf("min>max 應拒")
	}
	ads, err := st.ListAds("USDT", "TWD", "sell", 50)
	if err != nil {
		t.Fatalf("ListAds: %v", err)
	}
	if len(ads) != 1 || ads[0].Address != "tx0S" || ads[0].PaymentMethods[0] != "銀行轉帳" {
		t.Fatalf("廣告異常: %+v", ads)
	}
	if err := st.UpdateAdStatus(id, "tx0S", "paused"); err != nil {
		t.Fatalf("UpdateAdStatus: %v", err)
	}
	if err := st.UpdateAdStatus(id, "tx0X", "active"); err == nil {
		t.Fatalf("非廣告主應拒")
	}
	if ads, _ := st.ListAds("USDT", "TWD", "", 50); len(ads) != 0 {
		t.Fatalf("paused 廣告不應列出")
	}
}

func TestOrderLifecycle(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	adID, err := st.CreateAd("tx0S", "sell", "TACm", "TWD", 30, 1, 50, []string{"銀行轉帳"})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if _, err := st.CreateOrder(adID, "tx0S", "銀行轉帳", 10); err == nil {
		t.Fatalf("自己交易應拒")
	}
	o, err := st.CreateOrder(adID, "tx0B", "銀行轉帳", 10)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if o.TotalFiat != 300 || o.Status != StatusPending {
		t.Fatalf("訂單異常: %+v", o)
	}
	if _, err := st.CreateOrder(adID, "tx0C", "銀行轉帳", 100); err == nil {
		t.Fatalf("超限額應拒")
	}
	if err := st.ConfirmPayment(o.ID, "tx0X"); err == nil {
		t.Fatalf("非買家確認應拒")
	}
	if err := st.ConfirmPayment(o.ID, "tx0B"); err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	if err := st.Release(o.ID, "tx0B"); err == nil {
		t.Fatalf("非賣家放行應拒")
	}
	if err := st.Release(o.ID, "tx0S"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	got, _ := st.GetOrder(o.ID)
	if got.Status != StatusCompleted {
		t.Fatalf("應 completed: %+v", got)
	}
	ad, _ := st.GetAd(adID)
	if ad.CompletedOrders != 1 {
		t.Fatalf("完成數應 +1: %+v", ad)
	}
	// 取消：新單。
	o2, _ := st.CreateOrder(adID, "tx0B", "銀行轉帳", 5)
	if err := st.CancelOrder(o2.ID, "tx0Z"); err == nil {
		t.Fatalf("無權取消應拒")
	}
	if err := st.CancelOrder(o2.ID, "tx0B"); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if err := st.CancelOrder(o2.ID, "tx0B"); err == nil {
		t.Fatalf("二次取消應拒")
	}
	// 申訴：paid 後。
	o3, _ := st.CreateOrder(adID, "tx0B", "銀行轉帳", 5)
	if err := st.ConfirmPayment(o3.ID, "tx0B"); err != nil {
		t.Fatalf("ConfirmPayment: %v", err)
	}
	if err := st.DisputeOrder(o3.ID, "tx0B", "賣家未回應"); err != nil {
		t.Fatalf("DisputeOrder: %v", err)
	}
	got3, _ := st.GetOrder(o3.ID)
	if got3.Status != StatusDisputed || got3.DisputeReason != "賣家未回應" {
		t.Fatalf("申訴異常: %+v", got3)
	}
}
