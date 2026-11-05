package p2p

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"tacm/internal/chaindb"
	"tacm/internal/consensus/bft"
)

type mockHost struct{ h int64 }

func (m *mockHost) Height() int64                                          { return m.h }
func (m *mockHost) FinalizedHeight() int64                                 { return 0 }
func (m *mockHost) OnIncomingBlock(*chaindb.Block, []chaindb.Transaction) error { return nil }
func (m *mockHost) OnIncomingTx(map[string]any) error                      { return nil }
func (m *mockHost) OnIncomingVote(*bft.Vote) error                         { return nil }
func (m *mockHost) OnIncomingViewChange(int64, int32, string, string) error {
	return nil
}
func (m *mockHost) OnIncomingBridge(*BridgeGossip) error { return nil }
func (m *mockHost) BlockDetail(int64) (*chaindb.Block, []chaindb.Transaction, error) {
	return nil, nil, nil
}
func (m *mockHost) ReorgChain(int64, []chaindb.Block, [][]chaindb.Transaction) error {
	return nil
}

// TestHelloKeyRefreshRetry 驗證加密握手收到 400 後，會清除公鑰快取並重試成功。
func TestHelloKeyRefreshRetry(t *testing.T) {
	srvSecure, err := NewSecure()
	if err != nil {
		t.Fatal(err)
	}
	var helloCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /p2p/pubkey", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"pubkey": srvSecure.PubKeyHex()})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	mux.HandleFunc("POST /p2p/hello", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&helloCalls, 1)
		if n == 1 {
			writeP2PErr(w, http.StatusBadRequest, "stale key")
			return
		}
		writeP2PJSON(w, http.StatusOK, helloResp{OK: true, NodeID: "srv", BaseURL: ts.URL})
	})

	netw, err := New(Config{
		SelfID:  "cli",
		SelfURL: "http://127.0.0.1:9999",
		Encrypt: true,
	}, &mockHost{h: 1})
	if err != nil {
		t.Fatal(err)
	}
	netw.ctx = context.Background()
	body := []byte(`{"version":1,"node_id":"cli","base_url":"http://127.0.0.1:9999"}`)
	resp, err := netw.postHelloWithKeyRefresh(ts.URL+"/p2p/hello", body)
	if err != nil {
		t.Fatalf("expected retry success, got %v", err)
	}
	var got helloResp
	if err := json.Unmarshal(resp, &got); err != nil || !got.OK {
		t.Fatalf("bad response: %v", err)
	}
	if atomic.LoadInt32(&helloCalls) != 2 {
		t.Fatalf("expected exactly 2 hello calls, got %d", helloCalls)
	}
}

// TestHelloNetworkErrorNoRetry 驗證非 400 的網路錯誤不觸發重試。
func TestHelloNetworkErrorNoRetry(t *testing.T) {
	netw, err := New(Config{
		SelfID:  "cli2",
		SelfURL: "http://127.0.0.1:9998",
		Encrypt: false,
	}, &mockHost{h: 1})
	if err != nil {
		t.Fatal(err)
	}
	netw.ctx = context.Background()
	// 不存在的埠，連線失敗（非 400），不應重試、直接回錯。
	if _, err := netw.postHelloWithKeyRefresh("http://127.0.0.1:1/p2p/hello", []byte("{}")); err == nil {
		t.Fatal("expected error for unreachable peer")
	}
}
