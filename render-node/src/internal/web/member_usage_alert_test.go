package web

// member_usage_alert_test.go — 驗證高用量比例提醒（日10%/7日35%/30日80%）。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// alertDS 嵌入 fakeDS，覆寫用量與總資產。
type alertDS struct {
	fakeDS
	items []MemberLedgerItem
	asset float64
}

func (a *alertDS) MemberLedger(string, int) ([]MemberLedgerItem, error) { return a.items, nil }
func (a *alertDS) TACmBalanceFloat(string) (float64, error)             { return a.asset, nil }

func fetchUsage(t *testing.T, url string) memberUsage {
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var u memberUsage
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return u
}

func TestHighUsageAlerts(t *testing.T) {
	now := time.Now().Unix()
	items := []MemberLedgerItem{{Ts: now, Amount: 9, Memo: "titan:ai:assistant_turn:llama:high"}}
	ds := &alertDS{items: items, asset: 10} // 總資產 10 → 比例 90%，三門檻全達。
	srv, err := New(ds)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	u := fetchUsage(t, ts.URL+"/api/member/usage?address=tx01abc")
	if !u.OK || !u.HighUsage {
		t.Fatal("預期 high_usage")
	}
	if len(u.Alerts) != 3 {
		t.Fatalf("應有 3 則提醒，實際 %d", len(u.Alerts))
	}
	want := []string{
		"高用量提醒：單日消費用量已達TACm總資產10%",
		"高用量提醒：近7日消費用量已達TACm總資產35%",
		"高用量提醒：近30日消費已達總資產80% TACm將用盡請確保足夠資產！",
	}
	for i, w := range want {
		if u.Alerts[i].Message != w {
			t.Fatalf("提醒 %d 文案不符：%s", i, u.Alerts[i].Message)
		}
	}
}

func TestNoAlertBelowThreshold(t *testing.T) {
	now := time.Now().Unix()
	items := []MemberLedgerItem{{Ts: now, Amount: 0.05, Memo: "titan:ai:assistant_turn:rules:low"}}
	ds := &alertDS{items: items, asset: 10} // 0.5%，未達任何門檻。
	srv, _ := New(ds)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	u := fetchUsage(t, ts.URL+"/api/member/usage?address=tx01abc")
	if u.HighUsage || len(u.Alerts) != 0 {
		t.Fatal("低用量不應觸發提醒")
	}
}
