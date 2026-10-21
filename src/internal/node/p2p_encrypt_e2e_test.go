package node

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestP2PEncryptedGossip 全網開啟 -p2p-encrypt：交易經加密 gossip 轉發並打包，三節點賬本一致。
func TestP2PEncryptedGossip(t *testing.T) {
	aliceKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	alice := mustAddr(t, aliceKP)

	A := startFullNodeEnc(t, "nodeA", nil, false, alice)
	B := startFullNodeEnc(t, "nodeB", []string{A.url}, true, alice)
	C := startFullNodeEnc(t, "nodeC", []string{A.url}, true, alice)

	waitHeight(t, A.n, 3, 12*time.Second)
	waitHeight(t, B.n, 2, 12*time.Second)
	waitHeight(t, C.n, 2, 12*time.Second)

	bobKP, _ := crypto.GenerateKeyPair()
	bob := mustAddr(t, bobKP)
	tx := signedTx(t, aliceKP, bob, "50", "1", 0)

	body, _ := json.Marshal(tx)
	resp, err := http.Post(B.url+"/tx/submit", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		r, _ := io.ReadAll(resp.Body)
		t.Fatalf("提交交易失敗 %d: %s", resp.StatusCode, r)
	}
	resp.Body.Close()

	// 加密環境下交易仍應被 A 打包、全網同步。
	waitBalance(t, A.n, bob, 50, 20*time.Second)
	waitBalance(t, B.n, bob, 50, 20*time.Second)
	waitBalance(t, C.n, bob, 50, 20*time.Second)

	hA, hB, hC := A.n.DB().GetTipHeight(), B.n.DB().GetTipHeight(), C.n.DB().GetTipHeight()
	t.Logf("加密 P2P e2e 通過：bob 餘額三節點=50，高度 %d/%d/%d", hA, hB, hC)
}
