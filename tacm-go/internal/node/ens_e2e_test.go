package node

// ens_e2e_test.go — ENS 鏈上域名 e2e：
//  1) 註冊成功（年費 1 TACm 進獎勵池）
//  2) resolve/reverse/my 三向查詢
//  3) 年費不足 → 失敗且不寫入
//  4) 重名（未過期）→ 失敗
//  5) 指定 target 解析

import (
	"encoding/json"
	"net/http"
	"testing"

	"tacm/internal/wallet"
)

// getJSON GET helper（resolve/reverse/my 為 GET 端點）。
func ensGet(t *testing.T, url string) map[string]interface{} {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var m map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("解析響應 %s: %v", url, err)
	}
	return m
}

func TestENSE2E(t *testing.T) {
	n, srv := newTestRPCServer(t)
	waitHeight53(t, n, 2)

	user := "tx0ensuser"
	if err := n.walletSvc.Reward(user, wallet.AssetTACm,
		wallet.NewAmountF(100, wallet.AssetTACm), "test:ens"); err != nil {
		t.Fatalf("造資金: %v", err)
	}

	// 1) 註冊成功：年費 1 TACm → fee=1、ok=true。
	r := postJSON(t, srv.URL+"/api/ens/register",
		map[string]any{"name": "alice", "address": user, "years": 1})
	if r["ok"] != true {
		t.Fatalf("註冊失敗: %v", r)
	}
	if r["name"] != "alice.tac" {
		t.Fatalf("回傳名稱錯誤: %v", r["name"])
	}
	if r["fee"].(float64) != 1.0 {
		t.Fatalf("年費錯誤: %v", r["fee"])
	}
	if r["owner"] != user {
		t.Fatalf("owner 錯誤: %v", r["owner"])
	}

	// 2) resolve：alice → user。
	rr := ensGet(t, srv.URL+"/api/ens/resolve?name=alice")
	if rr["ok"] != true || rr["address"] != user {
		t.Fatalf("resolve 錯誤: %v", rr)
	}

	// 3) reverse：user → alice.tac。
	rv := ensGet(t, srv.URL+"/api/ens/reverse?address="+user)
	if rv["ok"] != true || rv["name"] != "alice.tac" {
		t.Fatalf("reverse 錯誤: %v", rv)
	}

	// 4) my：含 alice。
	my := ensGet(t, srv.URL+"/api/ens/my?address="+user)
	names, ok := my["names"].([]interface{})
	if !ok || len(names) != 1 {
		t.Fatalf("my 錯誤: %v", my)
	}

	// 5) 年費不足：無資金地址 → 失敗。
	poor := "tx0enspoor"
	r2 := postJSON(t, srv.URL+"/api/ens/register",
		map[string]any{"name": "bob", "address": poor, "years": 1})
	if r2["ok"] == true {
		t.Fatalf("年費不足應失敗: %v", r2)
	}

	// 6) 重名（未過期，不同 owner）→ 失敗。
	r3 := postJSON(t, srv.URL+"/api/ens/register",
		map[string]any{"name": "alice", "address": poor, "years": 1})
	if r3["ok"] == true {
		t.Fatalf("重名應失敗: %v", r3)
	}

	// 7) 續期（同 owner）：alice 續 2 年 → 到期延長。
	before := ensGet(t, srv.URL+"/api/ens/my?address="+user)
	namesB, _ := before["names"].([]interface{})
	expB := int64(0)
	if len(namesB) > 0 {
		if m, ok2 := namesB[0].(map[string]interface{}); ok2 {
			expB = int64(m["expires_at"].(float64))
		}
	}
	rr2 := postJSON(t, srv.URL+"/api/ens/register",
		map[string]any{"name": "alice", "address": user, "years": 2})
	if rr2["ok"] != true {
		t.Fatalf("續期失敗: %v", rr2)
	}
	if int64(rr2["expires_at"].(float64)) <= expB {
		t.Fatalf("續期到期未延長: before=%d after=%v", expB, rr2["expires_at"])
	}

	// 8) 指定 target：crypto0x 註冊 alice-eth 指向目标地址。
	tgt := "0x1234567890abcdef1234567890abcdef12345678"
	rt := postJSON(t, srv.URL+"/api/ens/register",
		map[string]any{"name": "alice-eth", "address": user, "target": tgt, "years": 1})
	if rt["ok"] != true || rt["target"] != tgt {
		t.Fatalf("target 註冊錯誤: %v", rt)
	}
	rrs := ensGet(t, srv.URL+"/api/ens/resolve?name=alice-eth")
	if rrs["ok"] != true || rrs["address"] != tgt {
		t.Fatalf("target resolve 錯誤: %v", rrs)
	}
}
