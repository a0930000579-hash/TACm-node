package governance

// v129：治理委託投票（delegateVote）單元測試。

import (
	"path/filepath"
	"testing"
)

func newDelegateTestGov(t *testing.T) *Governance {
	t.Helper()
	g, err := New(filepath.Join(t.TempDir(), "gov.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func TestDelegateBasic(t *testing.T) {
	g := newDelegateTestGov(t)
	alice := "tx0000000000000000000001"
	bob := "tx0000000000000000000002"
	carol := "tx0000000000000000000003"

	// 委託給自己報錯
	if err := g.Delegate(alice, alice, 10, ""); err == nil {
		t.Fatal("expected error delegating to self")
	}
	if err := g.Delegate(alice, bob, 10, "h1"); err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if d := g.DelegateeOf(alice); d != bob {
		t.Fatalf("DelegateeOf want %s got %s", bob, d)
	}
	// 覆寫委託
	if err := g.Delegate(alice, carol, 20, "h2"); err != nil {
		t.Fatalf("Delegate overwrite: %v", err)
	}
	if d := g.DelegateeOf(alice); d != carol {
		t.Fatalf("DelegateeOf want %s got %s", carol, d)
	}
	// 委託者集合
	dels := g.DelegatorsOf(carol)
	if len(dels) != 1 || dels[0] != alice {
		t.Fatalf("DelegatorsOf want [alice] got %v", dels)
	}
	// 解除委託
	if err := g.Undelegate(alice, 30, "h3"); err != nil {
		t.Fatalf("Undelegate: %v", err)
	}
	if d := g.DelegateeOf(alice); d != "" {
		t.Fatalf("DelegateeOf after undelegate want empty got %s", d)
	}
	// 重複解除報錯
	if err := g.Undelegate(alice, 31, "h4"); err == nil {
		t.Fatal("expected error undelegating twice")
	}
}

func TestDelegateList(t *testing.T) {
	g := newDelegateTestGov(t)
	alice := "tx0000000000000000000001"
	bob := "tx0000000000000000000002"
	carol := "tx0000000000000000000003"
	_ = g.Delegate(alice, bob, 10, "h1")
	_ = g.Delegate(carol, bob, 11, "h2")
	list, err := g.Delegations()
	if err != nil {
		t.Fatalf("Delegations: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("Delegations want 2 got %d", len(list))
	}
	if list[0].Delegatee != bob || list[1].Delegatee != bob {
		t.Fatalf("unexpected delegations: %+v", list)
	}
	// DelegatorsOf 聚合
	if dels := g.DelegatorsOf(bob); len(dels) != 2 {
		t.Fatalf("DelegatorsOf(bob) want 2 got %v", dels)
	}
	// 未委託者查詢
	if dels := g.DelegatorsOf(alice); len(dels) != 0 {
		t.Fatalf("DelegatorsOf(alice) want 0 got %v", dels)
	}
	if d := g.DelegateeOf("tx9999999999999999999999"); d != "" {
		t.Fatalf("DelegateeOf unknown want empty got %s", d)
	}
}
