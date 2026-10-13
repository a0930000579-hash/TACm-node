package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	bridgepkg "tacm/internal/bridge"
)

// TestDistributedBridgeNetworkedLockMint：2 節點 P2P，雙方為跨鏈守衛（門檻2）。
// A 發起鎖定-鑄造 → 提案經 P2P 廣播 → B 守衛簽名 → A 聚合 2 簽達標執行 →
// A 廣播執行結果 → B ApplyExecuted 收斂 confirmed，兩節點狀態一致。
func TestDistributedBridgeNetworkedLockMint(t *testing.T) {
	alice, _ := kpForTest(t)
	fnA := startFullNode(t, "nodeA", nil, false, alice)
	fnB := startFullNode(t, "nodeB", []string{fnA.url}, false, alice)

	guardA := bridgepkg.Validator{Address: fnA.n.Address(), Name: "guardian-A",
		PublicKey: fnA.n.IdentityPubHex(), Active: true}
	guardB := bridgepkg.Validator{Address: fnB.n.Address(), Name: "guardian-B",
		PublicKey: fnB.n.IdentityPubHex(), Active: true}
	vals := []bridgepkg.Validator{guardA, guardB}

	if err := fnA.n.AttachBridgeNetwork(vals); err != nil {
		t.Fatal(err)
	}
	if err := fnB.n.AttachBridgeNetwork(vals); err != nil {
		t.Fatal(err)
	}

	// 等待兩節點 P2P 互聯。
	waitPeers(t, fnA, 1)

	// A 發起跨鏈（含源鏈鎖定證明）。
	body := map[string]any{
		"source_chain": "tacm", "target_chain": "ethereum",
		"source_address": "tx0-source-alice", "target_address": "0xTargetAlice",
		"amount": 12.5, "token": "TACM",
		"source_tx_hash": "0xsource-lock-proof-001",
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(fnA.url+"/bridge/lock", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("發起跨鏈失敗: %s", resp.Status)
	}
	var init map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&init); err != nil {
		t.Fatal(err)
	}
	brid := init["bridge_tx_id"].(string)

	// A（提案方）聚合 B 的守衛簽名後應自動執行 confirmed。
	waitFor(t, func() bool {
		txA := bridgeStatus(t, fnA, brid)
		return txA != nil && txA.Status == bridgepkg.StatusConfirmed
	}, 15*time.Second, "A 側跨鏈未執行")

	// B 收到 exec 後收斂 confirmed，兩節點 target hash 一致。
	waitFor(t, func() bool {
		txB := bridgeStatus(t, fnB, brid)
		return txB != nil && txB.Status == bridgepkg.StatusConfirmed
	}, 15*time.Second, "B 側跨鏈未收斂")

	txA := bridgeStatus(t, fnA, brid)
	txB := bridgeStatus(t, fnB, brid)
	if txA.TargetTxHash == "" || txA.TargetTxHash != txB.TargetTxHash {
		t.Fatalf("兩節點 target hash 不一致: A=%q B=%q",
			txA.TargetTxHash, txB.TargetTxHash)
	}
	if len(txA.Signatures) == 0 {
		t.Fatal("執行記錄應包含守衛簽名")
	}
}

// 反向：銷毀-解鎖（B 發起，A 守衛）。
func TestDistributedBridgeNetworkedBurnUnlock(t *testing.T) {
	alice, _ := kpForTest(t)
	fnA := startFullNode(t, "nodeA", nil, false, alice)
	fnB := startFullNode(t, "nodeB", []string{fnA.url}, false, alice)

	guardA := bridgepkg.Validator{Address: fnA.n.Address(), Name: "guardian-A",
		PublicKey: fnA.n.IdentityPubHex(), Active: true}
	guardB := bridgepkg.Validator{Address: fnB.n.Address(), Name: "guardian-B",
		PublicKey: fnB.n.IdentityPubHex(), Active: true}
	vals := []bridgepkg.Validator{guardA, guardB}
	if err := fnA.n.AttachBridgeNetwork(vals); err != nil {
		t.Fatal(err)
	}
	if err := fnB.n.AttachBridgeNetwork(vals); err != nil {
		t.Fatal(err)
	}
	waitPeers(t, fnB, 1)

	body := map[string]any{
		"source_chain": "bsc", "target_chain": "tacm",
		"source_address": "0xSourceBob", "target_address": "tx0-target-bob",
		"amount": 3.0, "token": "TACM",
		"source_tx_hash": "0xsource-burn-proof-002",
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(fnB.url+"/bridge/burn", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("發起反向跨鏈失敗: %s", resp.Status)
	}
	var init map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&init); err != nil {
		t.Fatal(err)
	}
	brid := init["bridge_tx_id"].(string)

	waitFor(t, func() bool {
		txB := bridgeStatus(t, fnB, brid)
		return txB != nil && txB.Status == bridgepkg.StatusConfirmed
	}, 15*time.Second, "B 側反向跨鏈未執行")
	waitFor(t, func() bool {
		txA := bridgeStatus(t, fnA, brid)
		return txA != nil && txA.Status == bridgepkg.StatusConfirmed
	}, 15*time.Second, "A 側反向跨鏈未收斂")

	txA := bridgeStatus(t, fnA, brid)
	txB := bridgeStatus(t, fnB, brid)
	if txA.TargetTxHash == "" || txA.TargetTxHash != txB.TargetTxHash {
		t.Fatalf("兩節點 target hash 不一致: A=%q B=%q",
			txA.TargetTxHash, txB.TargetTxHash)
	}
}

// bridgeStatus 經 RPC 查詢跨鏈交易狀態。
func bridgeStatus(t *testing.T, fn *fullNode, bridgeTxID string) *bridgepkg.BridgeTx {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"bridge_tx_id": bridgeTxID})
	resp, err := http.Post(fn.url+"/bridge/status", "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var tx bridgepkg.BridgeTx
	if err := json.NewDecoder(resp.Body).Decode(&tx); err != nil {
		return nil
	}
	return &tx
}

func waitPeers(t *testing.T, fn *fullNode, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fn.url + "/p2p/peers")
		if err == nil {
			var out struct {
				Count int `json:"count"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if out.Count >= want {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("等待 P2P peer 超時")
}

// kpForTest 生成測試用賬戶地址。
func kpForTest(t *testing.T) (string, error) {
	t.Helper()
	return fmt.Sprintf("tx0-%d", time.Now().UnixNano()), nil
}
