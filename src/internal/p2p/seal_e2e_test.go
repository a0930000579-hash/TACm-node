package p2p

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/consensus/bft"
)

type hostStub struct{}

func (hostStub) Height() int64          { return 1 }
func (hostStub) FinalizedHeight() int64 { return 1 }
func (hostStub) OnIncomingBlock(*chaindb.Block, []chaindb.Transaction) error { return nil }
func (hostStub) OnIncomingTx(map[string]any) error { return nil }
func (hostStub) OnIncomingVote(*bft.Vote) error    { return nil }
func (hostStub) OnIncomingViewChange(int64, int32, string, string) error { return nil }
func (hostStub) OnIncomingBridge(*BridgeGossip) error { return nil }
func (hostStub) BlockDetail(int64) (*chaindb.Block, []chaindb.Transaction, error) { return nil, nil, nil }
func (hostStub) ReorgChain(int64, []chaindb.Block, [][]chaindb.Transaction) error { return nil }

func TestSealBodyHTTPFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mkN := func(id string) (*Network, *httptest.Server) {
		n, err := New(Config{SelfID: id, SelfURL: "http://x", Encrypt: true,
			HeartbeatInterval: time.Hour, SyncInterval: time.Hour}, hostStub{})
		if err != nil {
			t.Fatal(err)
		}
		n.Start(ctx)
		srv := httptest.NewServer(n.Handler())
		return n, srv
	}

	na, sa := mkN("A")
	defer sa.Close()
	nb, sb := mkN("B")
	defer sb.Close()

	hello, _ := json.Marshal(map[string]any{"node_id": "B", "base_url": sb.URL, "version": ProtocolVersion})
	sealed, err := nb.sealBody(sa.URL, hello)
	if err != nil {
		t.Fatalf("sealBody 失敗: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, sa.URL+"/p2p/hello", bytes.NewReader(sealed))
	resp, err := nb.client.Do(req)
	if err != nil {
		t.Fatalf("hello 請求失敗: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("A 解密 hello 失敗 status=%d body=%s", resp.StatusCode, bodyBytes)
	}
	hello2, _ := json.Marshal(map[string]any{"node_id": "A", "base_url": sa.URL, "version": ProtocolVersion})
	sealed2, err := na.sealBody(sb.URL, hello2)
	if err != nil {
		t.Fatalf("A sealBody 失敗: %v", err)
	}
	req2, _ := http.NewRequest(http.MethodPost, sb.URL+"/p2p/hello", bytes.NewReader(sealed2))
	resp2, err := na.client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("B 解密 hello 失敗 status=%d", resp2.StatusCode)
	}
	t.Log("加密 hello 雙向互通 OK")
}
