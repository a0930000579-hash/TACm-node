package node

import (
	"encoding/hex"
	"testing"
	"time"

	"tacm/internal/consensus/bft"
)

// TestDynamicValidatorsFromPOS：質押註冊後，BFT 驗證人集由 PoS 層動態重建。
func TestDynamicValidatorsFromPOS(t *testing.T) {
	cfg := testCfg(t)
	n, err := New(cfg, "dyn-node", 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	if n.posStore == nil {
		t.Fatal("pos 層未開啟")
	}
	insertGenesis(t, n)

	// 以本節點密鑰註冊為驗證人並自質押。
	pubhex := hex.EncodeToString(n.keypair.PublicKeyCompressed())
	if _, err := n.posStore.RegisterValidator(1, n.nodeAddress, "validator-a", 0.05, pubhex); err != nil {
		t.Fatalf("註冊驗證人: %v", err)
	}
	if _, err := n.posStore.Stake(1, n.nodeAddress, 5000, nil); err != nil {
		t.Fatalf("質押: %v", err)
	}
	if err := n.RebuildValidatorSetFromPOS(); err != nil {
		t.Fatalf("重建驗證人集: %v", err)
	}
	vs := n.ValidatorSet()
	if vs.Len() != 1 {
		t.Fatalf("動態驗證人數 = %d, want 1", vs.Len())
	}
	if vs.TotalPower() < 50 { // 5000 TACM / 100 = 50 power
		t.Errorf("動態權重 = %d, want ≥ 50", vs.TotalPower())
	}
	lst := n.ValidatorSet().List()
	if len(lst) != 1 || lst[0].Address != n.nodeAddress {
		t.Errorf("動態集地址錯誤: %+v", lst)
	}
}

// TestSlashingJailsValidator：雙重投票 → jail + 罰沒 → 驗證人集縮減。
func TestSlashingJailsValidator(t *testing.T) {
	cfg := testCfg(t)
	n, err := New(cfg, "slash-node", 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	insertGenesis(t, n)
	if n.posStore == nil {
		t.Fatal("pos 層未開啟")
	}

	// 兩個驗證人：A（本節點）、B（另一地址，pubkey 用 A 的公鑰僅供測試——bft.NewValidator
	// 校驗 address↔pubkey 一致，此處直接以 A 密鑰註冊 B 地址會失敗，故用本節點地址測試單驗證人
	// 場景：A 自質押後對同高度兩塊雙簽 → 被 jail + slash。
	pubhex := hex.EncodeToString(n.keypair.PublicKeyCompressed())
	if _, err := n.posStore.RegisterValidator(1, n.nodeAddress, "vA", 0.05, pubhex); err != nil {
		t.Fatalf("註冊: %v", err)
	}
	if _, err := n.posStore.Stake(1, n.nodeAddress, 2000, nil); err != nil {
		t.Fatalf("質押: %v", err)
	}
	if err := n.RebuildValidatorSetFromPOS(); err != nil {
		t.Fatalf("重建: %v", err)
	}

	// 雙簽：同高度同輪對兩個不同 block hash 投 precommit。
	h1, h2 := "aa", "bb"
	v1 := &bft.Vote{Type: "precommit", Height: 10, Round: 0, BlockHash: &h1, Validator: n.nodeAddress, Ts: time.Now().Unix()}
	v2 := &bft.Vote{Type: "precommit", Height: 10, Round: 0, BlockHash: &h2, Validator: n.nodeAddress, Ts: time.Now().Unix()}
	tr := bft.NewSlashingTracker(500)
	if tr.Observe(v1) != nil {
		t.Fatal("第一票不應觸發")
	}
	ev := tr.Observe(v2)
	if ev == nil {
		t.Fatal("第二票應觸發雙簽證據")
	}
	n.applySlashing(ev)

	v, err := n.posStore.GetValidatorByAddress(n.nodeAddress)
	if err != nil {
		t.Fatalf("查驗證人: %v", err)
	}
	if v.Status != "jailed" {
		t.Errorf("雙簽後應監禁，現為 %s", v.Status)
	}
	if v.TotalStake >= 2000 {
		t.Errorf("罰沒未生效：total_stake = %g", v.TotalStake)
	}
	evs, err := n.posStore.GetSlashEvents(10)
	if err != nil || len(evs) != 1 {
		t.Errorf("罰沒證據應 1 條: %d, err=%v", len(evs), err)
	}
}
