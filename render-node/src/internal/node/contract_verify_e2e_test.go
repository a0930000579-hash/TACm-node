package node

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// M140 合約 ABI/Source 驗證 e2e：
//  1. 部署代幣
//  2. POST /contract/verify 上傳 ABI
//  3. GET /contract/abi/{addr} 回讀 ABI
//  4. GET /contract/verified 清單
//  5. contracts_meta.json 持久化檔案存在
//  6. 錯誤路徑：未部署地址 404 / 非法 ABI 400

func TestContractVerifyFlow(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	out := studioPostJSON(t, srv, "/contract/deploy", map[string]any{
		"name": "VerifyToken", "symbol": "VFT", "supply": "5000",
	})
	if out["ok"] != true {
		t.Fatalf("部署失敗: %v", out)
	}
	addr := out["contract_address"].(string)
	addr0x, _ := tx0To0x(addr)
	waitFor(t, func() bool {
		info := n.contracts.Get(addr0x)
		return info != nil && info.CodeSize > 0
	}, 8*time.Second, "合約未入塊")

	abi := `[{"type":"function","name":"totalSupply","inputs":[],"outputs":[{"type":"uint256"}]},{"type":"function","name":"name","inputs":[],"outputs":[{"type":"string"}]},{"type":"function","name":"balanceOf","inputs":[{"type":"address"}],"outputs":[{"type":"uint256"}]}]`

	// 2. verify
	v := studioPostJSON(t, srv, "/contract/verify", map[string]any{
		"address": addr, "name": "VerifyToken", "source": "contract VerifyToken {}", "abi": abi,
	})
	if v["ok"] != true || v["verified"] != true {
		t.Fatalf("verify 失敗: %v", v)
	}
	if v["methods"].(float64) != 3 {
		t.Fatalf("methods 應為 3，得到 %v", v["methods"])
	}

	// 3. abi 回讀
	a := studioGetJSON(t, srv, "/contract/abi/"+addr)
	if a["abi"] != abi || a["name"] != "VerifyToken" {
		t.Fatalf("abi 回讀不符: %v", a)
	}

	// 4. verified 清單
	vl := studioGetJSON(t, srv, "/contract/verified")
	cnt, _ := vl["count"].(float64)
	if cnt != 1 {
		t.Fatalf("verified count 應為 1，得到 %v", vl)
	}

	// 5. 持久化檔案存在且含地址
	metaRaw, err := os.ReadFile(n.metaPath)
	if err != nil {
		t.Fatalf("contracts_meta.json 未寫出: %v", err)
	}
	if !strings.Contains(string(metaRaw), addr) {
		t.Fatalf("meta 檔缺地址 %s", addr)
	}

	// 6. 錯誤路徑：未部署地址 404
	resp, err := http.Get(srv.URL + "/contract/abi/0x000000000000000000000000000000000000dead")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未驗證合約應 404，得到 %d", resp.StatusCode)
	}

	// 非法 ABI 400
	b := studioPostJSON(t, srv, "/contract/verify", map[string]any{
		"address": addr, "name": "x", "abi": "not-json",
	})
	if b["ok"] == true {
		t.Fatalf("非法 ABI 應失敗: %v", b)
	}
}
