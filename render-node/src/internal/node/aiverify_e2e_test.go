package node

// aiverify_e2e_test.go — 跨鏈 AI 推理驗證 e2e：
//  A) 註冊 3 驗證者各質押 10 → submitter 提任務費 9 → 2 approve / 1 reject → verified；
//     正確兩者各分得 4.5、作惡者被沒收 1.0（檢查 StakeAccount/RewardAccount 餘額）。
//  B) 僅 2 票未達 MinVerifiers=3 → pending；過短挑戰窗口後 → expired，submitter 退費。
// 另驗錯誤路徑：非驗證者投票、重複投票。

import (
	"strconv"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"

	"tacm/internal/crypto"
	"tacm/internal/wallet"
)

// aivTestAddr 派生一組測試地址：回 (0x 地址, tx0 地址)。
func aivTestAddr(t *testing.T) (string, string) {
	t.Helper()
	key, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatalf("生成密鑰: %v", err)
	}
	pub := key.PubKey().SerializeUncompressed()
	h := crypto.Keccak256(pub[1:])
	tail := h[12:]
	return crypto.EthAddressHex(tail), crypto.Hash160ToAddress(tail)
}

func TestAIVerifySettleE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	v0x, v0tx := aivTestAddr(t)
	v1x, v1tx := aivTestAddr(t)
	v2x, v2tx := aivTestAddr(t)
	subx, subtx := aivTestAddr(t)

	// 造資金給三人 + submitter。
	for _, a := range []string{v0tx, v1tx, v2tx, subtx} {
		if err := n.walletSvc.Reward(a, wallet.AssetTACm,
			wallet.NewAmountF(100000, wallet.AssetTACm), "aiv:fund"); err != nil {
			t.Fatalf("造資金: %v", err)
		}
	}

	// 1) 三人各質押 10。
	for _, ax := range []string{v0x, v1x, v2x} {
		reg := postJSON(t, srv.URL+"/api/aiverify/verifier/register", map[string]any{
			"address": ax, "stake": 10,
		})
		if reg["ok"] != true {
			t.Fatalf("register %s: %v", ax, reg)
		}
	}
	// StakeAccount 應有 30。
	if aimTacmBal(t, n, "aiverify_stake").Cmp(wallet.NewAmountF(30, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("StakeAccount 應 30, got %s", aimTacmBal(t, n, "aiverify_stake").String())
	}

	// 2) submitter 提任務費 9 的驗證任務。
	sub := postJSON(t, srv.URL+"/api/aiverify/task/submit", map[string]any{
		"address": subx, "source_chain": "BSC", "model_id": 1,
		"input_hash": "0xinput", "expected_output_hash": "0xwant", "task_fee": 9,
	})
	if sub["ok"] != true {
		t.Fatalf("submit: %v", sub)
	}
	taskID := int64(sub["task"].(map[string]interface{})["id"].(float64))

	// 3) v0 approve、v1 approve（未達 quorum）。
	for _, ax := range []string{v0x, v1x} {
		r := postJSON(t, srv.URL+"/api/aiverify/attest", map[string]any{
			"address": ax, "task_id": taskID, "decision": "approve",
		})
		if r["ok"] != true {
			t.Fatalf("attest %s: %v", ax, r)
		}
		if r["settled"] == true {
			t.Fatalf("前兩票不應結算: %v", r)
		}
	}

	// 4) 非驗證者投票應失敗。
	roguex, _ := aivTestAddr(t)
	bad := postJSON(t, srv.URL+"/api/aiverify/attest", map[string]any{
		"address": roguex, "task_id": taskID, "decision": "approve",
	})
	if bad["ok"] == true {
		t.Fatalf("非驗證者應被拒絕: %v", bad)
	}
	// 重複投票應失敗。
	dup := postJSON(t, srv.URL+"/api/aiverify/attest", map[string]any{
		"address": v0x, "task_id": taskID, "decision": "approve",
	})
	if dup["ok"] == true {
		t.Fatalf("重複投票應被拒絕: %v", dup)
	}

	// 5) v2 reject（主張錯誤 output）→ 2/3 approve → verified。
	r2 := postJSON(t, srv.URL+"/api/aiverify/attest", map[string]any{
		"address": v2x, "task_id": taskID, "decision": "reject", "claim_hash": "0xfake",
	})
	if r2["ok"] != true || r2["settled"] != true {
		t.Fatalf("第三票應結算: %v", r2)
	}
	settle := r2["settle"].(map[string]interface{})
	if settle["status"] != "verified" {
		t.Fatalf("應 verified, got %v", settle["status"])
	}
	rewards := settle["rewards"].([]interface{})
	if len(rewards) != 2 {
		t.Fatalf("應 2 獎勵, got %d", len(rewards))
	}
	for _, rwi := range rewards {
		if rwi.(map[string]interface{})["amount"].(float64) != 4.5 {
			t.Fatalf("每人應得 4.5, got %v", rwi)
		}
	}
	slashes := settle["slashes"].([]interface{})
	if len(slashes) != 1 || slashes[0].(map[string]interface{})["slash"].(float64) != 1.0 {
		t.Fatalf("應 slash 1.0, got %+v", slashes)
	}

	// 6) 餘額驗證：StakeAccount = 30 - 1(slash) = 29；RewardAccount = 9 - 9(出獎) + 1(slash) = 1。
	if aimTacmBal(t, n, "aiverify_stake").Cmp(wallet.NewAmountF(29, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("StakeAccount 應 29, got %s", aimTacmBal(t, n, "aiverify_stake").String())
	}
	if aimTacmBal(t, n, "aiverify_reward").Cmp(wallet.NewAmountF(1, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("RewardAccount 應 1, got %s", aimTacmBal(t, n, "aiverify_reward").String())
	}

	// 7) v2 質押記錄 9、slashed_count=1。
	vs := getJSON(t, srv.URL+"/api/aiverify/verifiers")
	foundSlash := false
	for _, vi := range vs["verifiers"].([]interface{}) {
		row := vi.(map[string]interface{})
		if row["address"].(string) == v2tx {
			foundSlash = true
			if row["stake"].(float64) != 9.0 || row["slashed_count"].(float64) != 1 {
				t.Fatalf("v2 應 stake=9 slash=1, got %+v", row)
			}
		}
	}
	if !foundSlash {
		t.Fatalf("驗證者列表未含 v2")
	}
}

func TestAIVerifyExpireE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	v0x, v0tx := aivTestAddr(t)
	v1x, v1tx := aivTestAddr(t)
	subx, subtx := aivTestAddr(t)
	for _, a := range []string{v0tx, v1tx, subtx} {
		if err := n.walletSvc.Reward(a, wallet.AssetTACm,
			wallet.NewAmountF(100000, wallet.AssetTACm), "aiv:fund"); err != nil {
			t.Fatalf("造資金: %v", err)
		}
	}
	// 兩名驗證者質押。
	for _, ax := range []string{v0x, v1x} {
		postJSON(t, srv.URL+"/api/aiverify/verifier/register", map[string]any{"address": ax, "stake": 10})
	}

	// 提一個挑戰窗口 1 秒、任務費 7 的任務。
	sub := postJSON(t, srv.URL+"/api/aiverify/task/submit", map[string]any{
		"address": subx, "input_hash": "0xinput", "expected_output_hash": "0xwant", "task_fee": 7, "challenge_sec": 1,
	})
	if sub["ok"] != true {
		t.Fatalf("submit: %v", sub)
	}
	taskID := int64(sub["task"].(map[string]interface{})["id"].(float64))

	// 只投 2 票（MinVerifiers=3）→ pending。
	for _, ax := range []string{v0x, v1x} {
		postJSON(t, srv.URL+"/api/aiverify/attest", map[string]any{"address": ax, "task_id": taskID, "decision": "approve"})
	}
	got := getJSON(t, srv.URL+"/api/aiverify/task?id="+strconv.FormatInt(taskID, 10))
	if got["task"].(map[string]interface{})["status"] != "pending" {
		t.Fatalf("應 pending, got %+v", got["task"])
	}

	// 等過挑戰窗口，再讀列表（lazy 觸發過期退款）。
	time.Sleep(1200 * time.Millisecond)
	getJSON(t, srv.URL+"/api/aiverify/tasks")
	got = getJSON(t, srv.URL+"/api/aiverify/task?id="+strconv.FormatInt(taskID, 10))
	if got["task"].(map[string]interface{})["status"] != "expired" {
		t.Fatalf("應 expired, got %+v", got["task"])
	}
	// RewardAccount 收到 7 任務費、過期後全額退給 submitter → 歸零（證明退款發生）。
	if aimTacmBal(t, n, "aiverify_reward").Cmp(wallet.NewAmountF(0, wallet.AssetTACm).Big) != 0 {
		t.Fatalf("RewardAccount 應 0（過期退費）, got %s", aimTacmBal(t, n, "aiverify_reward").String())
	}
}
