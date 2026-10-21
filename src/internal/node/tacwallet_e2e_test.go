package node

// P1-② TACWallet 多簽合約錢包 e2e：
// 部署→addOwner×2→threshold=2→查詢面→approve 收集（alice 簽名）→重複/非 owner 拒絕→calldata。

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/vm"
)

func word32(x *big.Int) []byte {
	b := make([]byte, 32)
	x.FillBytes(b)
	return b
}

func TestTACWalletE2E(t *testing.T) {
	n, srv, _ := contractStudioSetup(t)

	// 1. 部署 TACWallet（節點代簽，部署者＝節點）。
	out := studioPostJSON(t, srv, "/tacwallet/deploy", map[string]any{})
	if out["ok"] != true {
		t.Fatalf("TACWallet 部署失敗: %v", out)
	}
	wlAddr := out["contract_address"].(string)
	wl0x, err := tx0To0x(wlAddr)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		info := n.contracts.Get(wl0x)
		return info != nil && info.CodeSize > 0
	}, 10*time.Second, "TACWallet 未入塊")

	// 2. status。
	st := studioGetJSON(t, srv, "/tacwallet/status")
	if st["deployed"] != true || st["address"] != wlAddr {
		t.Fatalf("tacwallet/status 異常: %v", st)
	}

	// 3. 生成 alice/bob/carol（owner 地址 = Hash160(pub)，與合約 ecrecover 一致）。
	aliceKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	carolKP, _ := crypto.GenerateKeyPair()
	alice, _ := aliceKP.Address()
	bob, _ := bobKP.Address()
	carol, _ := carolKP.Address()
	alice0x, _ := tx0To0x(alice)

	// 4. addOwner alice＋bob。
	for _, o := range []string{alice, bob} {
		r := studioPostJSON(t, srv, "/tacwallet/owner", map[string]any{"op": "add", "owner": o})
		if r["ok"] != true {
			t.Fatalf("addOwner(%s) 失敗: %v", o, r)
		}
	}

	// 5. 等鏈上生效：ownerCount=2。
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacwallet/query?op=ownerCount")
		c, _ := q["result"].(map[string]any)
		return c != nil && c["owner_count"].(float64) == 2
	}, 15*time.Second, "ownerCount 未達 2")

	// 6. threshold=2（需 ownerCount≥2 否則合約拒絕）。
	th := studioPostJSON(t, srv, "/tacwallet/threshold", map[string]any{"threshold": 2})
	if th["ok"] != true {
		t.Fatalf("threshold 失敗: %v", th)
	}
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacwallet/query?op=threshold")
		c, _ := q["result"].(map[string]any)
		return c != nil && c["threshold"].(float64) == 2
	}, 15*time.Second, "threshold 未達 2")
	q := studioGetJSON(t, srv, "/tacwallet/query?op=threshold")
	if res, _ := q["result"].(map[string]any); res["threshold"].(float64) != 2 {
		t.Fatalf("threshold 應為 2: %v", q)
	}
	qa := studioGetJSON(t, srv, "/tacwallet/query?op=isowner&owner="+alice)
	if res, _ := qa["result"].(map[string]any); res["is_owner"] != true {
		t.Fatalf("alice 應為 owner: %v", qa)
	}

	// 6. approve1（alice 簽名）→ approvals=1；錢包無餘額未達門檻不執行。
	to := alice0x
	value := big.NewInt(400)
	nonce := big.NewInt(0)
	txHash := vm.Keccak256(word32(toBig(to)), word32(value), word32(nonce))
	sig, err := crypto.SignEthereumMessage(txHash, aliceKP.PrivateKey())
	if err != nil || len(sig) != 65 {
		t.Fatalf("alice 簽名失敗: %v", err)
	}
	ap1 := studioPostJSON(t, srv, "/tacwallet/approve", map[string]any{
		"to": to, "value": value.String(), "nonce": nonce.String(),
		"v": sig[0], "r": "0x" + hex.EncodeToString(sig[1:33]), "s": "0x" + hex.EncodeToString(sig[33:65]),
	})
	if ap1["ok"] != true {
		t.Fatalf("approve1 失敗: %v", ap1)
	}
	waitFor(t, func() bool {
		q := studioGetJSON(t, srv, "/tacwallet/query?op=approvals&to="+to+"&value="+value.String()+"&nonce="+nonce.String())
		res, _ := q["result"].(map[string]any)
		return res != nil && res["approvals"].(float64) == 1
	}, 15*time.Second, "approvals 未達 1")

	// 7. 重複 approve（同 alice 簽名）→ 拒絕。
	sigDup, _ := crypto.SignEthereumMessage(txHash, aliceKP.PrivateKey())
	apDup := studioPostJSON(t, srv, "/tacwallet/approve", map[string]any{
		"to": to, "value": value.String(), "nonce": nonce.String(),
		"v": sigDup[0], "r": "0x" + hex.EncodeToString(sigDup[1:33]), "s": "0x" + hex.EncodeToString(sigDup[33:65]),
	})
	if apDup["ok"] == true {
		t.Fatalf("重複 approve 應被拒: %v", apDup)
	}

	// 8. 非 owner（carol）簽名 approve → 拒絕。
	sigC, _ := crypto.SignEthereumMessage(txHash, carolKP.PrivateKey())
	apC := studioPostJSON(t, srv, "/tacwallet/approve", map[string]any{
		"to": to, "value": value.String(), "nonce": nonce.String(),
		"v": sigC[0], "r": "0x" + hex.EncodeToString(sigC[1:33]), "s": "0x" + hex.EncodeToString(sigC[33:65]),
	})
	if apC["ok"] == true {
		t.Fatalf("非 owner 簽名 approve 應被拒: %v", apC)
	}

	// 9. calldata 端點：submit 組裝。
	cd := studioPostJSON(t, srv, "/tacwallet/calldata", map[string]any{
		"op": "submit", "to": bob, "value": "100",
	})
	if cd["ok"] != true || cd["to"] != wlAddr {
		t.Fatalf("calldata submit 異常: %v", cd)
	}
	data := strings.TrimPrefix(cd["data"].(string), "0x")
	raw, derr := hex.DecodeString(data)
	if derr != nil || len(raw) < 4 || raw[0] != 0xa8 || raw[1] != 0x8a || raw[2] != 0x4c || raw[3] != 0x89 {
		t.Fatalf("submit calldata selector 異常: %s", data)
	}

	// 10. isApproved 查詢（alice 已批准）。
	ia := studioGetJSON(t, srv, "/tacwallet/query?op=isApproved&to="+to+"&value="+value.String()+"&nonce="+nonce.String()+"&signer="+alice)
	if res, _ := ia["result"].(map[string]any); res["is_approved"] != true {
		t.Fatalf("isApproved(alice) 應為 true: %v", ia)
	}
	_ = carol
}

func toBig(s string) *big.Int {
	n, _ := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	return n
}
