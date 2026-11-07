package p2p

import "testing"

// 外連節點（無自身可達 URL）握手登記：以 NodeID 索引、保持在線，且不污染 urlIndex。
func TestRegisterPeer_EmptyURL(t *testing.T) {
	n := newTestNetwork()

	n.registerPeer("node-out-1", "", "tx01aaa", 100, 90)
	n.registerPeer("node-out-2", "", "tx01bbb", 80, 70)

	if len(n.peers) != 2 {
		t.Fatalf("want 2 peers got %d", len(n.peers))
	}
	if len(n.urlIndex) != 0 {
		t.Fatalf("empty-URL peers must not enter urlIndex, got %d", len(n.urlIndex))
	}
	for _, id := range []string{"node-out-1", "node-out-2"} {
		p, ok := n.peers[id]
		if !ok {
			t.Fatalf("peer %s not found", id)
		}
		if !p.Online || p.BaseURL != "" {
			t.Fatalf("peer %s want online with empty BaseURL, got online=%v url=%q", id, p.Online, p.BaseURL)
		}
	}

	// 可達節點仍正常進 urlIndex，與空 URL 節點共存。
	n.registerPeer("node-reach", "http://10.0.0.5:8080", "tx01ccc", 110, 100)
	if len(n.urlIndex) != 1 || n.urlIndex["http://10.0.0.5:8080"] != "node-reach" {
		t.Fatalf("reachable peer urlIndex mismatch: %#v", n.urlIndex)
	}
}

// filterDialable 只回帶可達 BaseURL 的節點，外連節點被跳過。
func TestFilterDialable(t *testing.T) {
	n := newTestNetwork()
	n.registerPeer("node-out", "", "tx01aaa", 100, 90)
	n.registerPeer("node-reach", "http://10.0.0.6:8080", "tx01bbb", 100, 90)

	got := filterDialable(n.PeerSnapshot())
	if len(got) != 1 || got[0].NodeID != "node-reach" {
		t.Fatalf("want only node-reach, got %#v", got)
	}
}
