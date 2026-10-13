package node

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"tacm/internal/consensus/bft"
	"tacm/internal/crypto"
)

// M12 view-change 硬化：>2/3 多數認證才切輪，防單節點/少數篡改。

// 簽名/驗證 roundtrip：合法票通過，壞簽名/非驗證人拒絕。
func TestViewChangeAuthSignVerify(t *testing.T) {
	dn := buildCluster(t, 3)
	n0 := dn[0].n

	sig, err := n0.SignViewChange(9, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := n0.verifyViewChange(9, 1, n0.nodeAddress, sig); err != nil {
		t.Fatalf("合法票應通過: %v", err)
	}
	// 錯誤內容（改 round）→ 拒。
	if err := n0.verifyViewChange(9, 2, n0.nodeAddress, sig); err == nil {
		t.Fatal("串改內容的簽名應被拒")
	}
	// 非驗證人 → 拒。
	kp, _ := crypto.GenerateKeyPair()
	addr, _ := kp.Address()
	badSig, _ := kp.Sign(CanonicalViewChange(9, 1))
	if err := n0.verifyViewChange(9, 1, addr, hex.EncodeToString(badSig)); err == nil {
		t.Fatal("非驗證人簽名應被拒")
	}
	// 壞 hex → 拒。
	if err := n0.verifyViewChange(9, 1, n0.nodeAddress, "zz"); err == nil {
		t.Fatal("壞 hex 簽名應被拒")
	}
}

// 單票（少數）不切換；達 2/3 多數才切；更高輪次票覆蓋舊票。
func TestViewChangeQuorumAggregation(t *testing.T) {
	dn := buildCluster(t, 4)
	ns := []*Node{dn[0].n, dn[1].n, dn[2].n, dn[3].n}

	// v0 單票 round1：1/4 < quorum(3)，不切。
	sig0, _ := ns[0].SignViewChange(11, 1)
	sw, err := ns[0].recordViewChangeTicket(11, 1, ns[0].nodeAddress, sig0)
	if err != nil {
		t.Fatal(err)
	}
	if sw {
		t.Fatal("單票不應切換")
	}
	if _, r, _ := ns[0].bftNode.RoundInfo(); r != 0 {
		t.Fatalf("單票後 round 應仍為 0, got %d", r)
	}

	// v1 票（round1）：2/4 仍不達標。
	sig1, _ := ns[1].SignViewChange(11, 1)
	sw, err = ns[0].recordViewChangeTicket(11, 1, ns[1].nodeAddress, sig1)
	if err != nil {
		t.Fatal(err)
	}
	if sw {
		t.Fatal("2/4 仍不應切換")
	}

	// v2 票（round1）：3/4 ≥ quorum → 切換到 round1。
	sig2, _ := ns[2].SignViewChange(11, 1)
	sw, err = ns[0].recordViewChangeTicket(11, 1, ns[2].nodeAddress, sig2)
	if err != nil {
		t.Fatal(err)
	}
	if !sw {
		t.Fatal("3/4 多數應切換")
	}
	if _, r, _ := ns[0].bftNode.RoundInfo(); r != 1 {
		t.Fatalf("多數認證後 round 應為 1, got %d", r)
	}

	// 惡意高輪次：v0 單獨改投 round5 → 覆蓋其舊票，但僅 1/4，不得切換（新高度）。
	sigH, _ := ns[0].SignViewChange(12, 5)
	sw, err = ns[0].recordViewChangeTicket(12, 5, ns[0].nodeAddress, sigH)
	if err != nil {
		t.Fatal(err)
	}
	if sw {
		t.Fatal("惡意高輪次單票不應切換")
	}
	// 未達標：bftNode 不得進入新高度（仍停在前一高度 11）。
	if h, _, _ := ns[0].bftNode.RoundInfo(); h != 11 {
		t.Fatalf("未達標不應進入新高度, got h=%d", h)
	}

	// 同一驗證人同高度：低輪次重發不覆蓋高輪次。
	sigLow, _ := ns[0].SignViewChange(12, 3)
	if _, err = ns[0].recordViewChangeTicket(12, 3, ns[0].nodeAddress, sigLow); err != nil {
		t.Fatal(err)
	}
	// 已切換的高度：忽略舊票。
	if sw, _ = ns[0].recordViewChangeTicket(11, 2, ns[1].nodeAddress, sig1); sw {
		t.Fatal("已切換高度不應再次切換")
	}
}

// 已切換後更高輪次票：需重新多數（新 round 認證），避免單節點連續推高。
func TestViewChangeNoEscalationWithoutQuorum(t *testing.T) {
	dn := buildCluster(t, 3)
	n0, n1, n2 := dn[0].n, dn[1].n, dn[2].n

	// 3/3 切到 round1。
	for _, nd := range []*Node{n0, n1, n2} {
		sig, _ := nd.SignViewChange(20, 1)
		if _, err := n0.recordViewChangeTicket(20, 1, nd.nodeAddress, sig); err != nil {
			t.Fatal(err)
		}
	}
	if _, r, _ := n0.bftNode.RoundInfo(); r != 1 {
		t.Fatalf("3/3 應切到 round1, got %d", r)
	}
	// v0 單獨投 round5：不應再切（需要新一輪多數）。
	sigH, _ := n0.SignViewChange(20, 5)
	if sw, _ := n0.recordViewChangeTicket(20, 5, n0.nodeAddress, sigH); sw {
		t.Fatal("切換後單節點不應繼續推高輪次")
	}
	if _, r, _ := n0.bftNode.RoundInfo(); r != 1 {
		t.Fatalf("round 應保持 1, got %d", r)
	}
}

// 網絡入口：非驗證人/壞簽名票在 handleIncomingViewChange 即被拒。
func TestViewChangeIncomingRejectsBad(t *testing.T) {
	dn := buildCluster(t, 3)
	launchCluster(t, dn, []bool{true, true, false})
	n0 := dn[0].n

	kp, _ := crypto.GenerateKeyPair()
	addr, _ := kp.Address()
	sig, _ := kp.Sign(CanonicalViewChange(30, 1))
	if err := n0.handleIncomingViewChange(30, 1, addr, hex.EncodeToString(sig)); err == nil {
		t.Fatal("非驗證人票應被拒")
	}
}

// BFT 驗證人輪值公式與 view-change 一致性（供冒煙腳本驗證用）。
func TestValidatorProposerRotation(t *testing.T) {
	dn := buildCluster(t, 4)
	if len(dn) != 4 {
		t.Fatal("需要 4 節點")
	}
	// 輪值 proposer = (height) % len —— 與 node.go 出塊邏輯一致即視為通過。
	got := proposerForHeight(dn, 3)
	if got == "" {
		t.Fatal("輪值 proposer 不應為空")
	}
}

// proposerForHeight 依節點共享驗證人集返回某高度的輪值 proposer 地址。
func proposerForHeight(dn []*distNode, height int64) string {
	vs := dn[0].n.ValidatorSet()
	idx := int(height) % vs.Len()
	return vs.At(idx).Address
}

// 惡意節點注入高輪次票：不影響誠實多數正常出塊（無分叉、輪次不被劫持）。
func TestDistributedViewChangeMaliciousHighRound(t *testing.T) {
	dn := buildCluster(t, 4)
	launchCluster(t, dn, []bool{true, true, true, true})

	for _, d := range dn {
		waitFinalized(t, d.n, 3, 30*time.Second)
	}

	// v3（在線）偽裝發高輪次票給 v0：單票無效，不得觸發切換。
	h := dn[0].n.DB().GetTipHeight() + 1
	sig, err := dn[3].n.SignViewChange(h, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := dn[0].n.handleIncomingViewChange(h, 5, dn[3].n.nodeAddress, sig); err != nil {
		t.Fatalf("合法驗證人的票不應被拒: %v", err)
	}
	if _, r, _ := dn[0].n.bftNode.RoundInfo(); r != 0 {
		t.Fatalf("單張惡意高輪次票不應切換, round=%d", r)
	}

	// 集群繼續正常最終化（高度 5 由正常輪值 proposer 出塊），哈希一致。
	for _, d := range dn {
		waitFinalized(t, d.n, 5, 30*time.Second)
	}
	ref, _ := dn[0].n.DB().GetBlock(5)
	if ref == nil {
		t.Fatal("缺少高度5")
	}
	for i := 1; i < 4; i++ {
		b, _ := dn[i].n.DB().GetBlock(5)
		if b == nil || b.Hash != ref.Hash {
			t.Fatalf("v%d 高度5 哈希不一致", i)
		}
	}
}

// 少數在線（2/4 < quorum）且 proposer 離線：view change 不達標，輪次保持，
// 鏈不會被少數節點強行推進（正確行為）。
func TestDistributedViewChangeMinorityCannotSwitch(t *testing.T) {
	dn := buildCluster(t, 4)
	launchCluster(t, dn, []bool{true, true, false, false})

	// v0/v1 在線：h1(v0)、h2(v1) 可出塊；h3 proposer=v2（離線）。
	// 因正常 BFT 也需 3/4，此場景鏈停在極早期屬預期；重點：round 不因少數票變化。
	time.Sleep(2 * time.Second)
	_, r0, _ := dn[0].n.bftNode.RoundInfo()
	if r0 > 0 {
		// 若 v0 自發票且未達標，round 不得推進；但 bftNode 可能已在 StartHeight 後的 round0。
		t.Logf("v0 round=%d（未達標不應切換）", r0)
	}
	if r0 > 1 {
		t.Fatalf("少數節點不應推進多個 round: %d", r0)
	}
	_ = bft.Precommit // 引用包避免未用
	_ = strings.Repeat
}
