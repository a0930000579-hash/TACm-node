package node

// P1-① TAC-ENS 鏈上命名服務 e2e：
// 部署註冊表→registerTo 代註冊（owner=用戶）→正向/反向查詢→calldata 組裝→衝突拒絕。

import (
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
)

func TestTACNSE2E(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// 1. 部署 TACNames 註冊表（節點代簽）。
	out := studioPostJSON(t, srv, "/tacns/deploy", map[string]any{})
	if out["ok"] != true {
		t.Fatalf("TACNames 部署失敗: %v", out)
	}
	nsAddr := out["contract_address"].(string)
	ns0x, err := tx0To0x(nsAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(ns0x)
		return info != nil && info.CodeSize > 0
	}, 10*time.Second, "TACNames 註冊表未入塊")

	// 2. 狀態端點。
	st := studioGetJSON(t, srv, "/tacns/status")
	if st["deployed"] != true || st["address"] != nsAddr {
		t.Fatalf("tacns/status 異常: %v", st)
	}

	// 3. 用戶地址與 registerTo 代註冊（owner=用戶）。
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice, _ := aliceKP.Address()
	alice0x, _ := tx0To0x(alice)
	reg := studioPostJSON(t, srv, "/tacns/register", map[string]any{
		"label": "titan", "years": 1, "from": alice,
	})
	if reg["ok"] != true {
		t.Fatalf("register 失敗: %v", reg)
	}

	// 4. 等待名稱入塊（owner=alice，0x 格式比較）。
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacns/query?label=titan")
		owner, _ := q["owner"].(string)
		return strings.EqualFold(owner, strings.ToLower(alice0x))
	}, 15*time.Second, "titan.tac 未入塊註冊")

	q := studioGetJSON(t, srv, "/tacns/query?label=titan")
	if q["exists"] != true || q["name"] != "titan.tac" {
		t.Fatalf("query label 異常: %v", q)
	}
	if exp, _ := q["expiry"].(string); exp == "" || exp == "0" {
		t.Fatalf("expiry 異常: %v", q)
	}

	// 5. 反向解析：alice → titan.tac 的 labelhash。
	q2 := studioGetJSON(t, srv, "/tacns/query?addr="+alice)
	rev, _ := q2["reverse_hash"].(string)
	if len(rev) != 66 || rev == "0x"+strings.Repeat("0", 64) {
		t.Fatalf("反向解析 hash 異常: %v", q2)
	}

	// 6. calldata 組裝（使用者自簽路徑）。
	cd := studioPostJSON(t, srv, "/tacns/calldata", map[string]any{
		"op": "setAddr", "label": "titan", "addr": alice,
	})
	if cd["ok"] != true || cd["to"] != nsAddr {
		t.Fatalf("calldata 異常: %v", cd)
	}
	data, _ := cd["data"].(string)
	if !strings.HasPrefix(data, "0x") || len(data) < 10 {
		t.Fatalf("calldata data 異常: %v", cd)
	}

	// 7. 名稱衝突：同 label 再次註冊應失敗（合約 revert）。
	dup := studioPostJSON(t, srv, "/tacns/register", map[string]any{
		"label": "titan", "years": 1, "from": alice,
	})
	if dup["ok"] == true {
		t.Fatalf("使用中的名稱應被拒，但 register 成功: %v", dup)
	}
}
