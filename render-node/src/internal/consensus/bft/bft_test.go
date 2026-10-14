package bft

import (
	"strings"
	"testing"

	"tacm/internal/crypto"
)

const testBlockHash = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func makeNodes(t *testing.T, vset *ValidatorSet, kps []*crypto.KeyPair, beh []string) []*BFTNode {
	t.Helper()
	nodes := make([]*BFTNode, len(kps))
	for i, kp := range kps {
		n, err := NewBFTNode(kp, vset, beh[i])
		if err != nil {
			t.Fatalf("NewBFTNode: %v", err)
		}
		nodes[i] = n
	}
	return nodes
}

func TestQuorumAndFaultTolerance(t *testing.T) {
	vset, _, err := MakeValidatorSet(4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if vset.TotalPower() != 4 {
		t.Errorf("total power = %d, want 4", vset.TotalPower())
	}
	if vset.QuorumPower() != 3 {
		t.Errorf("quorum = %d, want 3", vset.QuorumPower())
	}
	if vset.FaultTolerance() != 1 {
		t.Errorf("fault tolerance = %d, want 1", vset.FaultTolerance())
	}
	if !vset.HasQuorum(3) || vset.HasQuorum(2) {
		t.Error("quorum 判定錯誤")
	}
}

func TestProposerRotation(t *testing.T) {
	vset, _, err := MakeValidatorSet(4, nil)
	if err != nil {
		t.Fatal(err)
	}
	p0 := vset.Proposer(1, 0)
	p1 := vset.Proposer(1, 1)
	if p0.Address == p1.Address {
		t.Error("round 輪換應選不同 proposer")
	}
	if vset.Proposer(1, 4).Address != p0.Address {
		t.Error("輪換週期錯誤")
	}
}

func TestSignAndVerifyVote(t *testing.T) {
	vset, kps, err := MakeValidatorSet(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := SignVote(kps[0], Prevote, 5, 0, ptrString(testBlockHash))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyVote(v, vset); err != nil {
		t.Errorf("合法投票驗證失敗: %v", err)
	}
	// 篡改簽名
	tampered := *v
	tampered.Signature = "00"
	if err := VerifyVote(&tampered, vset); err == nil {
		t.Error("篡改簽名應被拒")
	}
	// 改高度 → 簽名不再匹配
	tampered2 := *v
	tampered2.Height = 6
	if err := VerifyVote(&tampered2, vset); err == nil {
		t.Error("內容被改應驗證失敗")
	}
}

func TestHonestFinality(t *testing.T) {
	vset, kps, _ := MakeValidatorSet(4, nil)
	beh := []string{"honest", "honest", "honest", "honest"}
	coord := NewBFTCoordinator(makeNodes(t, vset, kps, beh), vset, 3, 100)
	res := coord.RunHeight(1, testBlockHash, true)
	if !res.Committed {
		t.Fatalf("全誠實應最終化: rounds=%d", res.RoundsUsed)
	}
	if res.FinalizedHash == nil || *res.FinalizedHash != testBlockHash {
		t.Error("最終化哈希錯誤")
	}
}

func TestEquivocationDetectedAndSlashed(t *testing.T) {
	vset, kps, _ := MakeValidatorSet(4, nil)
	beh := []string{"equivocate", "honest", "honest", "honest"}
	nodes := makeNodes(t, vset, kps, beh)
	coord := NewBFTCoordinator(nodes, vset, 3, 100)
	res := coord.RunHeight(1, testBlockHash, true)

	if len(res.Evidence) < 2 {
		t.Fatalf("prevote 與 precommit 各應檢測一次雙簽，實得 %d", len(res.Evidence))
	}
	ev := res.Evidence[0]
	if ev.Validator != nodes[0].Address() {
		t.Error("證據應指向 equivocator")
	}
	if res.Slashed[nodes[0].Address()] != 200 {
		t.Errorf("兩次雙簽罰沒應為 200，實得: %v", res.Slashed)
	}
	// equivocator 仍投了正確塊，鏈照常最終化
	if !res.Committed {
		t.Error("含一個 equivocator（<1/3）仍應最終化")
	}
}

func TestOneOfflineStillFinalizes(t *testing.T) {
	vset, kps, _ := MakeValidatorSet(4, nil)
	beh := []string{"offline", "honest", "honest", "honest"}
	coord := NewBFTCoordinator(makeNodes(t, vset, kps, beh), vset, 3, 100)
	res := coord.RunHeight(1, testBlockHash, true)
	if !res.Committed {
		t.Error("1/4 離線（<1/3）仍應最終化")
	}
}

func TestTooManyOfflineNoFinality(t *testing.T) {
	vset, kps, _ := MakeValidatorSet(4, nil)
	beh := []string{"offline", "offline", "honest", "honest"}
	coord := NewBFTCoordinator(makeNodes(t, vset, kps, beh), vset, 2, 100)
	res := coord.RunHeight(1, testBlockHash, true)
	if res.Committed {
		t.Error("2/4 離線（≥1/3）不應最終化")
	}
}

func TestWrongBlockHonestReject(t *testing.T) {
	vset, kps, _ := MakeValidatorSet(4, nil)
	beh := []string{"wrongblock", "honest", "honest", "honest"}
	coord := NewBFTCoordinator(makeNodes(t, vset, kps, beh), vset, 1, 100)
	res := coord.RunHeight(1, testBlockHash, true)
	// 提議者投偽造塊，誠實節點不會在第一輪跟著最終化偽造塊
	if res.Committed && res.FinalizedHash != nil &&
		strings.HasPrefix(*res.FinalizedHash, "0badc0de") {
		t.Error("不應最終化偽造塊")
	}
}

func TestFinalityLog(t *testing.T) {
	vset, kps, _ := MakeValidatorSet(4, nil)
	beh := []string{"honest", "honest", "honest", "honest"}
	coord := NewBFTCoordinator(makeNodes(t, vset, kps, beh), vset, 3, 100)
	log := NewFinalityLog()

	res := coord.RunHeight(1, testBlockHash, true)
	if !log.Record(res) {
		t.Error("首次記錄應成功")
	}
	if log.Record(res) {
		t.Error("重複記錄應失敗")
	}
	if !log.IsFinalized(1) || log.IsFinalized(2) {
		t.Error("最終化查詢錯誤")
	}
	latest, ok := log.Latest()
	if !ok || latest != 1 {
		t.Error("latest 錯誤")
	}
}

func TestValidatorAddressMismatch(t *testing.T) {
	kp, _ := crypto.GenerateKeyPair()
	other, _ := crypto.GenerateKeyPair()
	otherAddr, _ := other.Address()
	_, err := NewValidator(otherAddr, hexEncode(kp.PublicKeyCompressed()), 1)
	if err == nil {
		t.Error("地址公鑰不匹配應報錯")
	}
}
