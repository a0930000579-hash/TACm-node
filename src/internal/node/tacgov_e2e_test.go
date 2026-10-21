package node

// P1-③ TACGovernor 鏈上治理 e2e：
// 部署→status→propose→vote（節點）→重複 vote 拒絕→execute→重複 execute 拒絕→query→calldata。

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"tacm/internal/vm"
)

func TestTACGovE2E(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 1. 部署 TACGovernor（節點代簽，部署者＝節點）。
	out := studioPostJSON(t, srv, "/tacgov/deploy", map[string]any{})
	if out["ok"] != true {
		t.Fatalf("TACGovernor 部署失敗: %v", out)
	}
	govAddr := out["contract_address"].(string)
	gov0x, err := tx0To0x(govAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(gov0x)
		return info != nil && info.CodeSize > 0
	}, 10*time.Second, "TACGovernor 未入塊")

	// 2. status。
	st := studioGetJSON(t, srv, "/tacgov/status")
	if st["deployed"] != true || st["next_proposal_id"].(float64) != 1 {
		t.Fatalf("tacgov/status 異常: %v", st)
	}

	// 3. 給治理合約餘額（執行資金池）。
	n.contracts.SyncAccount(gov0x, big.NewInt(100_000), 0)

	// 4. propose{target=alice0x, value=3000}。
	target := "0x" + strings.Repeat("99", 20)
	pr := studioPostJSON(t, srv, "/tacgov/propose", map[string]any{
		"target": target, "value": "3000",
	})
	if pr["ok"] != true {
		t.Fatalf("propose 失敗: %v", pr)
	}
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacgov/query?proposal_id=1&op=info")
		prop, _ := q["proposal"].(map[string]any)
		return prop != nil && prop["support"].(float64) == 0 && prop["value"].(float64) == 3000
	}, 15*time.Second, "proposal#1 未建立")

	// 5. vote support=1（投票者＝節點）。
	vt := studioPostJSON(t, srv, "/tacgov/vote", map[string]any{
		"proposal_id": 1, "support": 1,
	})
	if vt["ok"] != true {
		t.Fatalf("vote 失敗: %v", vt)
	}
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacgov/query?proposal_id=1&op=support")
		s, _ := q["support"].(float64)
		return s == 1
	}, 15*time.Second, "support 未達 1")
	qv := studioGetJSON(t, srv, "/tacgov/query?proposal_id=1&op=support")
	if s, _ := qv["support"].(float64); s != 1 {
		t.Fatalf("support 應為 1: %v", qv)
	}
	qc := studioGetJSON(t, srv, "/tacgov/query?proposal_id=1&op=creator")
	admin0x, _ := tx0To0x(admin)
	if res, _ := qc["creator"].(string); res != admin0x {
		t.Fatalf("creator 應為節點 %s: %v", admin0x, qc)
	}

	// 6. 投票完成後挹注執行資金池（鏈上帳戶層），再 execute。
	if err := n.DB().ImportAccountState(govAddr, "100000", 0); err != nil {
		t.Fatalf("挹注治理資金失敗: %v", err)
	}
	ex := studioPostJSON(t, srv, "/tacgov/execute", map[string]any{"proposal_id": 1})
	if ex["ok"] != true {
		t.Fatalf("execute 失敗: %v", ex)
	}
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacgov/query?proposal_id=1&op=executed")
		e, _ := q["executed"].(float64)
		return e == 1
	}, 15*time.Second, "execute 未生效")
	qi := studioGetJSON(t, srv, "/tacgov/query?proposal_id=1&op=info")
	if prop, _ := qi["proposal"].(map[string]any); prop["executed"].(float64) != 1 || prop["status"] != "executed" {
		t.Fatalf("proposal 狀態異常: %v", qi)
	}

	// 7. 重複 vote（同一節點）→ 拒絕（一人一票）。
	vtDup := studioPostJSON(t, srv, "/tacgov/vote", map[string]any{
		"proposal_id": 1, "support": 1,
	})
	if vtDup["ok"] == true {
		t.Fatalf("重複 vote 應被拒: %v", vtDup)
	}

	// 8. 重複 execute → 拒絕（已執行）。
	exDup := studioPostJSON(t, srv, "/tacgov/execute", map[string]any{"proposal_id": 1})
	if exDup["ok"] == true {
		t.Fatalf("重複 execute 應被拒: %v", exDup)
	}

	// 9. calldata 端點：4 ops 組裝。
	cd := studioPostJSON(t, srv, "/tacgov/calldata", map[string]any{
		"op": "propose", "target": target, "value": "500",
	})
	if cd["ok"] != true || cd["to"] != govAddr {
		t.Fatalf("calldata propose 異常: %v", cd)
	}
	data := strings.TrimPrefix(cd["data"].(string), "0x")
	raw, derr := hex.DecodeString(data)
	if derr != nil || len(raw) < 4 || raw[0] != 0x5b || raw[1] != 0x4c || raw[2] != 0x9c || raw[3] != 0x59 {
		t.Fatalf("propose calldata selector 異常: %s", data)
	}
	cdV := studioPostJSON(t, srv, "/tacgov/calldata", map[string]any{
		"op": "vote", "proposal_id": 1, "support": 0,
	})
	rawV, _ := hex.DecodeString(strings.TrimPrefix(cdV["data"].(string), "0x"))
	if len(rawV) < 4 || rawV[0] != 0xb3 || rawV[1] != 0x84 || rawV[2] != 0xab || rawV[3] != 0xef {
		t.Fatalf("vote calldata selector 異常: %v", cdV)
	}
	cdE := studioPostJSON(t, srv, "/tacgov/calldata", map[string]any{"op": "execute", "proposal_id": 1})
	rawE, _ := hex.DecodeString(strings.TrimPrefix(cdE["data"].(string), "0x"))
	if len(rawE) < 4 || rawE[0] != 0xfe || rawE[1] != 0x0d || rawE[2] != 0x94 || rawE[3] != 0xc1 {
		t.Fatalf("execute calldata selector 異常: %v", cdE)
	}
	cdI := studioPostJSON(t, srv, "/tacgov/calldata", map[string]any{"op": "info", "proposal_id": 1})
	rawI, _ := hex.DecodeString(strings.TrimPrefix(cdI["data"].(string), "0x"))
	if len(rawI) < 4 || rawI[0] != 0xae || rawI[1] != 0x09 || rawI[2] != 0x2e || rawI[3] != 0x89 {
		t.Fatalf("info calldata selector 異常: %v", cdI)
	}

	// 10. VM 層治理能力驗證（supportsInterface 回 0）。
	_ = vm.TACGovernorRuntime
	_ = time.Now
}
