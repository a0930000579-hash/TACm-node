package node

import (
	"testing"
	"time"

	"tacm/internal/crypto"
)

// 本檔驗證 M135 鏈上多代幣（ERC-1155）RPC 閉環：
//  1. POST /contract/erc1155/deploy 部署多代幣合約（name/symbol）
//  2. POST /contract/erc1155/mint   mint(to, id, amount)，入塊後 balanceOf 變動
//  3. POST /contract/erc1155/transfer safeTransferFrom(from, to, id, amount)
//  4. POST /contract/erc1155/batch-transfer safeBatchTransferFrom 多 id 批次轉移
//  5. POST /contract/erc1155/set-approval-for-all ＋ isApprovedForAll 查詢
//  6. supportsInterface（0xd9b67a26 / 0x01ffc9a7 / 未知）驗證

func TestContractErc1155DeployMintTransferBatch(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)

	// 1. 部署多代幣合約。
	out := studioPostJSON(t, srv, "/contract/erc1155/deploy", map[string]any{
		"name": "TAC Items", "symbol": "TACI",
	})
	if out["ok"] != true {
		t.Fatalf("ERC-1155 部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	contract0x, err := tx0To0x(contractAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "ERC-1155 合約未入塊部署")

	// 2. 便利查詢初始（name/symbol 對照）。
	info := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr)
	if info["ok"] != true {
		t.Fatalf("ERC-1155 查詢失敗: %v", info)
	}
	if info["name"] != "TAC Items" || info["symbol"] != "TACI" {
		t.Fatalf("ERC-1155 元資料異常: %v", info)
	}

	// 3. mint id=1 amount=100、id=2 amount=50 給節點帳戶（caller==from 免授權）。
	for _, m := range []struct {
		id, amt string
	}{{"1", "100"}, {"2", "50"}} {
		mm := studioPostJSON(t, srv, "/contract/erc1155/mint", map[string]any{
			"contract": contractAddr, "to": admin,
			"id": m.id, "amount": m.amt,
		})
		if mm["ok"] != true {
			t.Fatalf("mint id=%s 失敗: %v", m.id, mm)
		}
	}

	// balanceOf(admin, 1) 應等 100、id=2 應等 50（兩筆 mint 依序入塊，等待兩筆皆上鏈）。
	waitFor(t, func() bool {
		b1 := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+admin+"&id=1")
		if b1["ok"] != true || b1["balance_of"] != "100" {
			return false
		}
		b2 := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+admin+"&id=2")
		return b2["ok"] == true && b2["balance_of"] == "50"
	}, 8*time.Second, "balanceOf(admin,1/2) 未達 100/50")
	if b := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+admin+"&id=2"); b["balance_of"] != "50" {
		t.Fatalf("balanceOf(admin,2) 應為 50，得到 %v", b["balance_of"])
	}

	// 4. safeTransferFrom(admin→bob, 1, 30)。
	bobKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bob, _ := bobKP.Address()
	tr := studioPostJSON(t, srv, "/contract/erc1155/transfer", map[string]any{
		"contract": contractAddr, "from": admin, "to": bob,
		"id": "1", "amount": "30",
	})
	if tr["ok"] != true {
		t.Fatalf("safeTransferFrom 失敗: %v", tr)
	}
	waitFor(t, func() bool {
		b := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+bob+"&id=1")
		return b["ok"] == true && b["balance_of"] == "30"
	}, 8*time.Second, "bob id1 未達 30")
	if b := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+admin+"&id=1"); b["balance_of"] != "70" {
		t.Fatalf("admin id1 應為 70，得到 %v", b["balance_of"])
	}

	// 5. safeBatchTransferFrom(admin→bob, ids=[1,2], amounts=[10,20])。
	bt := studioPostJSON(t, srv, "/contract/erc1155/batch-transfer", map[string]any{
		"contract": contractAddr, "from": admin, "to": bob,
		"ids": []string{"1", "2"}, "amounts": []string{"10", "20"},
	})
	if bt["ok"] != true {
		t.Fatalf("safeBatchTransferFrom 失敗: %v", bt)
	}
	waitFor(t, func() bool {
		b := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+bob+"&id=2")
		return b["ok"] == true && b["balance_of"] == "20"
	}, 8*time.Second, "bob id2 未達 20")
	if b := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+admin+"&id=1"); b["balance_of"] != "60" {
		t.Fatalf("admin id1 應為 60，得到 %v", b["balance_of"])
	}
	if b := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?holder="+admin+"&id=2"); b["balance_of"] != "30" {
		t.Fatalf("admin id2 應為 30，得到 %v", b["balance_of"])
	}

	// 6. balanceOfBatch（admin/bob × id=1）→ [60, 40]。
	bb := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+
		"?holder="+admin+"&id=1&holder2="+bob+"&id2=1")
	if bb["ok"] != true {
		t.Fatalf("balanceOfBatch 查詢失敗: %v", bb)
	}
	batch, ok := bb["balance_of_batch"].([]any)
	if !ok || len(batch) != 2 || batch[0] != "60" || batch[1] != "40" {
		t.Fatalf("balanceOfBatch 應為 [60 40]，得到 %v", bb["balance_of_batch"])
	}
}

func TestContractErc1155ApprovalAndInterface(t *testing.T) {
	n, srv, admin := contractStudioSetup(t)
	out := studioPostJSON(t, srv, "/contract/erc1155/deploy", map[string]any{
		"name": "TAC Items", "symbol": "TACI",
	})
	if out["ok"] != true {
		t.Fatalf("ERC-1155 部署失敗: %v", out)
	}
	contractAddr := out["contract_address"].(string)
	contract0x, err := tx0To0x(contractAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(contract0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "ERC-1155 合約未入塊部署")

	opKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	op, _ := opKP.Address()

	// 1. setApprovalForAll(admin→op, true) → isApprovedForAll=1。
	sa := studioPostJSON(t, srv, "/contract/erc1155/set-approval-for-all", map[string]any{
		"contract": contractAddr, "owner": admin, "operator": op, "approved": true,
	})
	if sa["ok"] != true {
		t.Fatalf("setApprovalForAll 失敗: %v", sa)
	}
	waitFor(t, func() bool {
		a := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?owner="+admin+"&operator="+op)
		return a["ok"] == true && a["is_approved_for_all"] == true
	}, 8*time.Second, "isApprovedForAll 未變 1")

	// 2. 取消授權 → 0。
	sa2 := studioPostJSON(t, srv, "/contract/erc1155/set-approval-for-all", map[string]any{
		"contract": contractAddr, "owner": admin, "operator": op, "approved": false,
	})
	if sa2["ok"] != true {
		t.Fatalf("取消授權失敗: %v", sa2)
	}
	waitFor(t, func() bool {
		a := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?owner="+admin+"&operator="+op)
		return a["ok"] == true && a["is_approved_for_all"] == false
	}, 8*time.Second, "isApprovedForAll 未變 0")

	// 3. supportsInterface：ERC-1155（0xd9b67a26）=1、ERC-165（0x01ffc9a7）=1、未知=0。
	a := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?iface=0xd9b67a26")
	if a["supports_interface"] != true {
		t.Fatalf("supportsInterface(0xd9b67a26) 應為 true，得到 %v", a)
	}
	if a := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?iface=0x01ffc9a7"); a["supports_interface"] != true {
		t.Fatalf("supportsInterface(0x01ffc9a7) 應為 true，得到 %v", a["supports_interface"])
	}
	if a := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?iface=0xffffffff"); a["supports_interface"] != false {
		t.Fatalf("supportsInterface(未知) 應為 false，得到 %v", a["supports_interface"])
	}

	// 4. uri(id=1) 應有回傳值（metadata 預留）。
	if a := studioGetJSON(t, srv, "/contract/erc1155/"+contractAddr+"?uri_id=1"); a["uri"] == nil {
		t.Fatalf("uri(id=1) 應有回傳，得到 %v", a)
	}
}
