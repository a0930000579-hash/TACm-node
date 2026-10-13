package node

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/bridge"
	"tacm/internal/crypto"
)

// bscStubE2E 為 e2e 的 BSC JSON-RPC 樁（與 bridge 包測試一致的最小實作）。
type bscStubE2E struct {
	t         *testing.T
	rawTxs    []string
	blockNum  string
	logs      []bridge.BSClog
	tokenAddr string
	lockProxy string
}

func (s *bscStubE2E) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.t.Fatal(err)
	}
	var res any
	switch req.Method {
	case "eth_chainId":
		res = "0x61"
	case "eth_blockNumber":
		if s.blockNum == "" {
			res = "0x64"
		} else {
			res = s.blockNum
		}
	case "eth_getTransactionCount":
		res = "0x0"
	case "eth_gasPrice":
		res = "0x3b9aca00"
	case "eth_estimateGas":
		res = "0x5208"
	case "eth_sendRawTransaction":
		raw, _ := req.Params[0].(string)
		s.rawTxs = append(s.rawTxs, raw)
		res = "0x" + strings.Repeat("f2", 32)
	case "eth_getLogs":
		res = s.logs
	default:
		s.t.Fatalf("未預期方法 %s", req.Method)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": res})
}

func TestBSCRelayStatusEndpoint(t *testing.T) {
	n, srv := startBridgeNode(t)
	defer srv.Close()
	defer n.Close()

	// 未啟用時回 disabled。
	sc, body := httpJSON(t, "GET", srv.URL+"/bridge/bsc/status", nil)
	if sc != 200 || body["enabled"] != false {
		t.Fatalf("未啟用應 disabled: %d %v", sc, body)
	}

	st := &bscStubE2E{t: t}
	bscSrv := httptest.NewServer(http.HandlerFunc(st.handler))
	defer bscSrv.Close()

	if err := n.StartBSCRelay(bridge.RelayerConfig{
		BSCTestRPC:       bscSrv.URL,
		BSCPrivateKeyHex: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BSCChainID:       97,
		BSCTokenAddr:     "0x" + "11" + strings.Repeat("00", 18) + "22",
		BSCLockProxyAddr: "0x" + "33" + strings.Repeat("00", 18) + "44",
		PollInterval:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	sc, body = httpJSON(t, "GET", srv.URL+"/bridge/bsc/status", nil)
	if sc != 200 || body["enabled"] != true {
		t.Fatalf("啟用後應 enabled: %d %v", sc, body)
	}
	if numField(body["chain_id"]) != 97 {
		t.Fatalf("chain_id=%v", body["chain_id"])
	}
	if !strings.HasPrefix(body["signer_addr"].(string), "0x") {
		t.Fatalf("signer_addr=%v", body["signer_addr"])
	}
}

func TestBSCRelayMintEndToEnd(t *testing.T) {
	n, srv := startBridgeNode(t)
	defer srv.Close()
	defer n.Close()

	st := &bscStubE2E{t: t}
	bscSrv := httptest.NewServer(http.HandlerFunc(st.handler))
	defer bscSrv.Close()

	if err := n.StartBSCRelay(bridge.RelayerConfig{
		BSCTestRPC:       bscSrv.URL,
		BSCPrivateKeyHex: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		BSCChainID:       97,
		BSCTokenAddr:     "0x" + "11" + strings.Repeat("00", 18) + "22",
		BSCLockProxyAddr: "0x" + "33" + strings.Repeat("00", 18) + "44",
		PollInterval:     150 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	src := crypto.Hash160ToAddress(crypto.Hash160([]byte("e2e-src")))
	tgt := crypto.Hash160ToAddress(crypto.Hash160([]byte("e2e-tgt")))

	// 建 tacm→bsc 交易＋確認鎖定。
	sc, lock := httpJSON(t, "POST", srv.URL+"/bridge/lock", map[string]any{
		"source_chain": "tacm", "target_chain": "bsc",
		"source_address": src, "target_address": tgt,
		"amount": 25, "token": "TACM",
	})
	if sc != 200 {
		t.Fatalf("lock: %d %v", sc, lock)
	}
	id := lock["bridge_tx_id"].(string)
	sc, cl := httpJSON(t, "POST", srv.URL+"/bridge/confirm-lock",
		map[string]any{"bridge_tx_id": id, "tx_hash": "0x" + strings.Repeat("a3", 32)})
	if sc != 200 || cl["status"] != "locked" {
		t.Fatalf("confirm-lock: %d %v", sc, cl)
	}

	// 等 relay 自動 mint（mock BSC 收到 raw 且橋狀態推進）。
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(st.rawTxs) >= 1 {
			// 驗證 calldata 為 mint selector。
			raw, err := crypto.HexDecode(strings.TrimPrefix(st.rawTxs[0], "0x"))
			if err != nil {
				t.Fatal(err)
			}
			dec, err := crypto.DecodeEthRawTx(raw)
			if err != nil {
				t.Fatal(err)
			}
			sel := crypto.ABISelector(bridge.SigMint)
			if len(dec.Data) != 68 || dec.Data[0] != sel[0] || dec.Data[1] != sel[1] {
				t.Fatalf("calldata=%x", dec.Data)
			}
			// 金額 25 TACm 扣 0.1% 費 → 24.975e18 wei。
			want, _ := new(big.Int).SetString("24975000000000000000", 10)
			got := new(big.Int).SetBytes(dec.Data[36:68])
			if got.Cmp(want) != 0 {
				t.Fatalf("mint 金額=%s 期望 %s", got, want)
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(st.rawTxs) == 0 {
		t.Fatal("relay 未在時限內發送 BSC mint")
	}
	// 橋狀態推進（直接查節點橋，避免 GET body 限制）。
	waitFor(t, func() bool {
		bt, err := n.bridge.Status(id)
		if err != nil || bt == nil {
			return false
		}
		return bt.Status == "minted" || bt.Status == "confirmed"
	}, 4*time.Second, "BSC mint 未推進橋狀態")
	// 掃描進度端點可用。
	sc, _ = httpJSON(t, "GET", srv.URL+"/bridge/bsc/status", nil)
	if sc != 200 {
		t.Fatalf("bsc status: %d", sc)
	}
}
