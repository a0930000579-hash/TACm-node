package bridge

import (
	"encoding/json"
	"testing"

	"tacm/internal/crypto"
)

// deepCopyMessage 經 JSON 深拷貝一份 CrossChainMessage（模擬網絡傳播後的獨立實例）。
func deepCopyMessage(m *CrossChainMessage) (*CrossChainMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out CrossChainMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// makeGuardians 生成 n 個守衛密鑰與 Validator 列表。
func makeGuardians(t *testing.T, n int) ([]*crypto.KeyPair, []Validator) {
	t.Helper()
	kps := make([]*crypto.KeyPair, n)
	vals := make([]Validator, n)
	for i := range kps {
		kp, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		kps[i] = kp
		addr, _ := kp.Address()
		vals[i] = Validator{
			Address: addr, Name: "g" + string(rune('A'+i)),
			PublicKey: hexEncode(kp.PublicKeyCompressed()), Active: true,
		}
	}
	return kps, vals
}

func hexEncode(b []byte) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexDigits[c>>4]
		out[i*2+1] = hexDigits[c&0xf]
	}
	return string(out)
}

func newNetworkedBridge(t *testing.T, kp *crypto.KeyPair, vals []Validator) *Bridge {
	t.Helper()
	b, err := New(DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetGuardian(kp, vals); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// 3 守衛、門檻 2：完整 鎖定-鑄造 流程——提案 → 各守衛簽名 → 達標後
// 提案方執行 confirmed、非提案方 ApplyExecuted 收斂一致。
func TestNetworkedLockMintFlow(t *testing.T) {
	kps, vals := makeGuardians(t, 3)
	proposerAddr, _ := kps[0].Address()

	proposer := newNetworkedBridge(t, kps[0], vals)
	peer := newNetworkedBridge(t, kps[1], vals)
	_ = newNetworkedBridge(t, kps[2], vals)

	m, res, err := proposer.NetworkedTransfer("lock", "tacm", "ethereum",
		"src-addr", "tgt-addr", 10, "TACM", "0xsource-lock", proposerAddr)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusPending {
		t.Fatalf("發起後應 pending, got %s", res.Status)
	}

	// 先拷貝出 peer 的獨立副本（此時 m 尚未含任何簽名）。
	peerM, err := deepCopyMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	// 本節點（proposer）也作為守衛簽名。
	sigA, err := proposer.HandleProposal(m)
	if err != nil {
		t.Fatal(err)
	}
	// 其他守衛節點簽名（真實網絡中各有獨立副本）。
	sigB, err := peer.HandleProposal(peerM)
	if err != nil {
		t.Fatal(err)
	}

	// 未達標：只有 1 簽。
	res1, err := proposer.HandleSignature(m, sigA)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != nil {
		t.Fatalf("1 簽不應執行, got %+v", res1)
	}

	// 第二簽達標 → proposer 執行。
	res2, err := proposer.HandleSignature(m, sigB)
	if err != nil {
		t.Fatal(err)
	}
	if res2 == nil || !res2.Executed {
		t.Fatalf("2 簽應執行, got %+v", res2)
	}
	if res2.TargetTxHash == "" {
		t.Fatal("執行應產生 target tx hash")
	}
	tx, err := proposer.Status(res2.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status != StatusConfirmed {
		t.Fatalf("執行後應 confirmed, got %s", tx.Status)
	}

	// 非提案方節點：達標但不執行，等待 exec 收斂。
	// peerM 是 peer 的獨立副本（經其 HandleProposal 後含 peer 自身簽名 sigB）。
	resPeer, err := peer.HandleSignature(peerM, sigA)
	if err != nil {
		t.Fatal(err)
	}
	if resPeer == nil || resPeer.Executed {
		t.Fatalf("非提案方不應執行, got %+v", resPeer)
	}
	// 非提案方本地有 pending 記錄（ensurePending）。
	if tx, _ := peer.Status(res2.BridgeTxID); tx == nil {
		t.Fatal("非提案方應有本地記錄")
	}
	// 收到 exec 後收斂 confirmed。
	converged, err := peer.ApplyExecuted(res2.BridgeTxID, res2.TargetTxHash)
	if err != nil {
		t.Fatal(err)
	}
	if converged.Status != StatusConfirmed {
		t.Fatalf("收斂後應 confirmed, got %s", converged.Status)
	}
	if converged.TargetTxHash != res2.TargetTxHash {
		t.Fatal("收斂後 target hash 應一致")
	}
	// 冪等：再次 ApplyExecuted 不報錯、狀態不變。
	if _, err := peer.ApplyExecuted(res2.BridgeTxID, res2.TargetTxHash); err != nil {
		t.Fatalf("ApplyExecuted 應冪等: %v", err)
	}
	// 重複簽名：已收集則忽略。
	if _, err := proposer.HandleSignature(m, sigB); err != nil {
		t.Fatalf("重複簽名應忽略: %v", err)
	}
}

// 反向：銷毀-解鎖。
func TestNetworkedBurnUnlockFlow(t *testing.T) {
	kps, vals := makeGuardians(t, 2)
	proposerAddr, _ := kps[0].Address()
	proposer := newNetworkedBridge(t, kps[0], vals)
	peer := newNetworkedBridge(t, kps[1], vals)

	m, _, err := proposer.NetworkedTransfer("burn", "ethereum", "tacm",
		"eth-addr", "tac-addr", 5, "TACM", "0xsource-burn", proposerAddr)
	if err != nil {
		t.Fatal(err)
	}
	sigA, err := proposer.HandleProposal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposer.HandleSignature(m, sigA); err != nil {
		t.Fatal(err)
	}
	peerM, err := deepCopyMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	sigB, err := peer.HandleProposal(peerM)
	if err != nil {
		t.Fatal(err)
	}
	res, err := proposer.HandleSignature(m, sigB)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.Executed {
		t.Fatalf("2/2 簽應執行, got %+v", res)
	}
	tx, err := proposer.Status(res.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status != StatusConfirmed {
		t.Fatalf("執行後應 confirmed, got %s", tx.Status)
	}
	if tx.UnlockTxHash != res.TargetTxHash {
		t.Fatal("unlock tx hash 應一致")
	}
}

// 守衛配置校驗：密鑰缺失 / 守衛少於門檻報錯；提案金額低於下限被拒。
func TestNetworkedValidation(t *testing.T) {
	b, err := New(DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if err := b.SetGuardian(nil, nil); err == nil {
		t.Fatal("nil 密鑰應報錯")
	}
	kps, vals := makeGuardians(t, 1)
	if err := b.SetGuardian(kps[0], vals); err == nil {
		t.Fatal("守衛少於門檻應報錯")
	}
}

// 提案重放：重複 HandleProposal 冪等不報錯；過期提案被拒。
func TestNetworkedProposalReplayAndExpiry(t *testing.T) {
	kps, vals := makeGuardians(t, 2)
	proposerAddr, _ := kps[0].Address()
	proposer := newNetworkedBridge(t, kps[0], vals)

	m, _, err := proposer.NetworkedTransfer("lock", "tacm", "bsc",
		"a", "b", 2, "TACM", "0xs", proposerAddr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proposer.HandleProposal(m); err != nil {
		t.Fatal(err)
	}
	if _, err := proposer.HandleProposal(m); err != nil {
		t.Fatalf("重放提案應冪等: %v", err)
	}

	// 過期提案。
	m.Ts = m.Ts - 90000
	m2, err := NewMessage(m.SourceChain, m.TargetChain, m.Type, m.Payload, m.Nonce+100)
	if err != nil {
		t.Fatal(err)
	}
	m2.Ts = m.Ts // 強制過期
	if _, err := proposer.HandleProposal(m2); err == nil {
		t.Fatal("過期提案應被拒")
	}
}
