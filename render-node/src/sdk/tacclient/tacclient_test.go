package tacclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubNode 模擬節點 RPC：GET /status、/account、/tx/submit。
func stubNode(t *testing.T, submitCheck func(map[string]any) error) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id": "n1", "address": "tx0node", "network": "tacm-test",
			"chain_id": "8888", "block_height": 12, "final_block_height": 12,
			"mempool_size": 0, "effective_difficulty": 1,
			"consensus": "pow_bft_distributed", "emission_model": "halving",
		})
	})
	mux.HandleFunc("/account/", func(w http.ResponseWriter, r *http.Request) {
		addr := strings.TrimPrefix(r.URL.Path, "/account/")
		bal, nonce := "0", int64(0)
		if addr == "tx0alice" {
			bal, nonce = "999.5", 7
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"address": addr, "balance": bal, "nonce": nonce,
		})
	})
	mux.HandleFunc("/tx/submit", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if submitCheck != nil {
			if err := submitCheck(body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "tx_hash": "0xabc"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestGenerateKeyAndAddress(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.Address(), "tx0") {
		t.Fatalf("地址前綴異常: %s", k.Address())
	}
	k2, err := KeyFromPrivateKeyHex(k.PrivateKeyHex())
	if err != nil {
		t.Fatal(err)
	}
	if k2.Address() != k.Address() {
		t.Fatalf("hex 往返地址不一致: %s vs %s", k2.Address(), k.Address())
	}
	if k2.PrivateKeyHex() != k.PrivateKeyHex() {
		t.Fatal("hex 往返私鑰不一致")
	}
	if len(k.PublicKeyHex()) != 66 { // 33 bytes compressed
		t.Fatalf("壓縮公鑰 hex 長度 = %d", len(k.PublicKeyHex()))
	}
}

func TestClientQueries(t *testing.T) {
	ts := stubNode(t, nil)
	c := NewClient(ts.URL)

	st, err := c.Status()
	if err != nil || st.BlockHeight != 12 {
		t.Fatalf("Status err=%v height=%d", err, st.BlockHeight)
	}
	h, err := c.LatestHeight()
	if err != nil || h != 12 {
		t.Fatalf("LatestHeight err=%v h=%d", err, h)
	}
	bal, err := c.Balance("tx0alice")
	if err != nil || bal != "999.5" {
		t.Fatalf("Balance err=%v bal=%s", err, bal)
	}
	nonce, err := c.Nonce("tx0alice")
	if err != nil || nonce != 7 {
		t.Fatalf("Nonce err=%v nonce=%d", err, nonce)
	}
	// 未知地址 → 零餘額。
	b2, err := c.Balance("tx0nobody")
	if err != nil || b2 != "0" {
		t.Fatalf("空帳戶 Balance err=%v bal=%s", err, b2)
	}
}

func TestTransferSignsAndSubmits(t *testing.T) {
	var got map[string]any
	ts := stubNode(t, func(tx map[string]any) error {
		got = tx
		return nil
	})
	c := NewClient(ts.URL)
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	hash, err := c.Transfer(k, "tx0bob", "10", "0.1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if hash != "0xabc" {
		t.Fatalf("hash=%s", hash)
	}
	if got["from"] != k.Address() || got["to"] != "tx0bob" {
		t.Fatalf("from/to 異常: %v", got)
	}
	if got["nonce"] != float64(0) { // stub 對非 alice 帳戶回 nonce=0
		t.Fatalf("nonce=%v", got["nonce"])
	}
	sig, _ := got["signature"].(string)
	if sig == "" {
		t.Fatal("缺少簽名")
	}
	pub, _ := got["pubkey"].(string)
	if pub != k.PublicKeyHex() {
		t.Fatal("pubkey 與金鑰不一致")
	}
	// 簽名內容不含 pubkey（TxSighash 白名單外）——節點驗證時以 pubkey 欄位獨立核對。
	if got["memo"] != "hello" {
		t.Fatalf("memo=%v", got["memo"])
	}
}
