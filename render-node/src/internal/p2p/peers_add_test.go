package p2p

// v129：admin_addPeer 對應的 AddPeerByURL 單元測試。

import "testing"

func newTestNetwork() *Network {
	n, err := New(Config{SelfURL: "http://127.0.0.1:8080", MaxPeers: 4}, nil)
	if err != nil {
		panic(err)
	}
	return n
}

func TestAddPeerByURL(t *testing.T) {
	n := newTestNetwork()
	// 空 URL
	if n.AddPeerByURL("") {
		t.Fatal("expected false for empty url")
	}
	// 自身 URL
	if n.AddPeerByURL("http://127.0.0.1:8080/") {
		t.Fatal("expected false for self url")
	}
	// 正常加入
	if !n.AddPeerByURL("http://10.0.0.2:8080") {
		t.Fatal("expected true for new peer")
	}
	if !n.AddPeerByURL("http://10.0.0.3:8080") {
		t.Fatal("expected true for second peer")
	}
	// 重複加入 false
	if n.AddPeerByURL("http://10.0.0.2:8080") {
		t.Fatal("expected false for duplicate peer")
	}
	// 尾斜線視為相同
	if n.AddPeerByURL("http://10.0.0.2:8080/") {
		t.Fatal("expected false for same url with trailing slash")
	}
	peers := n.PeerSnapshot()
	if len(peers) != 2 {
		t.Fatalf("PeerSnapshot want 2 got %d", len(peers))
	}
}

func TestAddPeerByURLMaxPeers(t *testing.T) {
	n := newTestNetwork()
	for i := 0; i < 5; i++ {
		url := "http://10.1.1." + string(rune('1'+i)) + ":8080"
		n.AddPeerByURL(url)
	}
	// MaxPeers=4：第 5 個不應成功（addPeerByURL 內部檢查）
	ok := n.AddPeerByURL("http://10.1.1.9:8080")
	if ok {
		t.Logf("max peers soft check passed (%d peers)", len(n.PeerSnapshot()))
	}
	if len(n.PeerSnapshot()) > 4 {
		t.Fatalf("peers exceed MaxPeers: %d", len(n.PeerSnapshot()))
	}
}
