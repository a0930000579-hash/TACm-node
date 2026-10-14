package bft

import (
	"testing"
)

// MoveToRound 應能推進輪次並重置投票/提議，且拒絕倒退與 committed。
func TestMoveToRound(t *testing.T) {
	vset, kps, err := MakeValidatorSet(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes := makeNodes(t, vset, kps, []string{BehaviorHonest})
	bn := nodes[0]

	bn.StartHeight(1)
	h, r, step := bn.RoundInfo()
	if h != 1 || r != 0 || step != StepPropose {
		t.Fatalf("初始狀態錯誤: h=%d r=%d step=%s", h, r, step)
	}

	// 超時推進 +1。
	bn.MoveToNextRound()
	_, r, _ = bn.RoundInfo()
	if r != 1 {
		t.Fatalf("MoveToNextRound 後 round=%d, want 1", r)
	}

	// 響應網絡 view-change 直接跳到更高輪。
	bn.MoveToRound(3)
	_, r, _ = bn.RoundInfo()
	if r != 3 {
		t.Fatalf("MoveToRound 後 round=%d, want 3", r)
	}
	if st := bn.state; st != nil && len(st.prevotes) != 0 {
		t.Fatal("輪次切換後應清空 prevotes")
	}
	if st := bn.state; st != nil && st.proposalHash != nil {
		t.Fatal("輪次切換後應清空 proposal")
	}

	// 拒絕倒退。
	bn.MoveToRound(2)
	if _, r, _ := bn.RoundInfo(); r != 3 {
		t.Fatalf("倒退輪次不應生效: round=%d", r)
	}
}

// committed 後 MoveToRound 不得生效。
func TestMoveToRoundRejectedAfterCommit(t *testing.T) {
	vset, kps, err := MakeValidatorSet(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes := makeNodes(t, vset, kps, []string{BehaviorHonest})
	bn := nodes[0]

	bn.StartHeight(1)
	bh := testBlockHash
	bn.ReceiveProposal(&bh, true)
	bn.Advance() // prevote
	bn.Advance() // precommit + commit
	if !bn.Committed() {
		t.Fatal("應已 commit")
	}
	bn.MoveToRound(5)
	_, r, _ := bn.RoundInfo()
	if r != 0 {
		t.Fatalf("committed 後 round 應保持 0, got %d", r)
	}
}
