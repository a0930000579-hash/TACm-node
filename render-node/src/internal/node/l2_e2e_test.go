package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"tacm/internal/config"
	"tacm/internal/crypto"
)

func httpJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	out := map[string]any{}
	_ = dec.Decode(&out)
	return res.StatusCode, out
}

// numField 兼容 JSON 數字、字符串數字與 float64。
func numField(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case float64:
		return x
	}
	return 0
}

func signedL2Map(t *testing.T, kp *crypto.KeyPair, to string,
	amount, fee float64, nonce int64) map[string]any {
	t.Helper()
	addr, _ := kp.Address()
	m := map[string]any{
		"from": addr, "to": to,
		"amount": strconv.FormatFloat(amount, 'f', -1, 64),
		"fee":    strconv.FormatFloat(fee, 'f', -1, 64),
		"nonce":  nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	sig, err := crypto.SignTransaction(m, kp.PrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	m["signature"] = sig
	return m
}

func TestL2RollupEndToEnd(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.BlockTime = 1
	cfg.EnableL2 = true

	n, err := New(cfg, "l2node", 2)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRPCServer(n).Handler())
	defer srv.Close()
	n.Start()
	defer n.Close()

	// M58：coinbase 瓜分寫入區塊——無「開機」礦工時全數入池；模擬「節點開機挖礦」，
	// 讓節點地址收到 88% 瓜分，以支付 L1 提交費。M60：開機＝鏈上 hb:on。
	if err := n.walletSvc.Store().RegisterMiner(n.nodeAddress, 1, 1); err != nil {
		t.Fatal(err)
	}
	hr, err := n.walletSvc.Store().MinerHashrate(n.nodeAddress)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.SubmitHeartbeat(n.nodeAddress, true, hr); err != nil {
		t.Fatal(err)
	}

	// 等節點 coinbase 累積，足以支付 L1 提交費。
	deadline := time.Now().Add(8 * time.Second)
	var nodeBal float64
	for time.Now().Before(deadline) {
		_, acc := httpJSON(t, "GET", srv.URL+"/account/"+n.nodeAddress, nil)
		nodeBal = numField(acc["balance"])
		if nodeBal > 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if nodeBal <= 1 {
		t.Fatalf("節點 coinbase 未累積: %g", nodeBal)
	}

	userKP, _ := crypto.GenerateKeyPair()
	bobKP, _ := crypto.GenerateKeyPair()
	user, _ := userKP.Address()
	bob, _ := bobKP.Address()

	// L1→L2 存款（憑 L1 鎖定憑證）。
	sc, dep := httpJSON(t, "POST", srv.URL+"/l2/deposit",
		map[string]any{"address": user, "amount": 1000, "l1_tx_hash": "l1lock-1"})
	if sc != 200 {
		t.Fatalf("L2 存款失敗: %v", dep)
	}

	// 用戶簽名 L2 轉賬 100、費1。
	sc, sub := httpJSON(t, "POST", srv.URL+"/l2/tx",
		signedL2Map(t, userKP, bob, 100, 1, 0))
	if sc != 200 {
		t.Fatalf("L2 交易提交失敗: %v", sub)
	}

	// 手動批量打包。
	sc, batch := httpJSON(t, "POST", srv.URL+"/l2/batch", nil)
	if sc != 200 {
		t.Fatalf("批量打包失敗: %v", batch)
	}

	// 提交狀態根到 L1（in-process，真實 L1 交易）。
	sc, l1sub := httpJSON(t, "POST", srv.URL+"/l2/submit",
		map[string]any{"height": 1})
	if sc != 200 {
		t.Fatalf("提交 L1 失敗: %v", l1sub)
	}

	// L2 狀態與賬戶核對。
	_, st := httpJSON(t, "GET", srv.URL+"/l2/status", nil)
	bh, _ := st["block_height"].(json.Number)
	if h, _ := bh.Int64(); h < 1 {
		t.Fatalf("L2 塊高應≥1: %v", st)
	}
	if root, _ := st["state_root"].(string); root == "" || root == rollupZeroRoot {
		t.Fatalf("狀態根不應為零根: %v", st)
	}
	_, userAcc := httpJSON(t, "GET", srv.URL+"/l2/account/"+user, nil)
	if v := numField(userAcc["balance"]); v != 899 {
		t.Errorf("用戶 L2 應899, got %g", v)
	}
	_, bobAcc := httpJSON(t, "GET", srv.URL+"/l2/account/"+bob, nil)
	if v := numField(bobAcc["balance"]); v != 100 {
		t.Errorf("bob L2 應100, got %g", v)
	}

	// 誠實塊挑戰應被駁回。
	sc, ch := httpJSON(t, "POST", srv.URL+"/l2/challenge",
		map[string]any{"height": 1})
	if sc != 200 {
		t.Fatalf("挑戰請求失敗: %v", ch)
	}
	if ok, _ := ch["challenge_success"].(bool); ok {
		t.Error("誠實塊不應被判欺詐")
	}

	// L2→L1 提款。
	sc, wd := httpJSON(t, "POST", srv.URL+"/l2/withdraw",
		map[string]any{"address": user, "amount": 200})
	if sc != 200 {
		t.Fatalf("提款失敗: %v", wd)
	}
	_, userAcc2 := httpJSON(t, "GET", srv.URL+"/l2/account/"+user, nil)
	if v := numField(userAcc2["balance"]); v != 699 {
		t.Errorf("提款後用戶應699, got %g", v)
	}
}

const rollupZeroRoot = "0x" +
	"0000000000000000000000000000000000000000000000000000000000000000"
