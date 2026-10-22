package node

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"tacm/internal/config"
	"tacm/internal/consensus/bft"
	"tacm/internal/crypto"
	"tacm/internal/p2p"
)

type distNode struct {
	id       string
	n        *Node
	l        net.Listener
	port     int
	url      string
	netw     *p2p.Network
	srv      *http.Server
	launched bool
}

// buildCluster 創建 N 個節點（各自密鑰、創世），並用全部節點構造共享
// 全局驗證人集（各 power1），但尚未啟動網絡/共識。
func buildCluster(t *testing.T, n int) []*distNode {
	t.Helper()
	dn := make([]*distNode, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("v%d", i)
		l, port := listenPort(t)
		cfg := config.Default()
		cfg.DataDir = t.TempDir()
		cfg.BlockTime = 1
		node, err := New(cfg, id, 1)
		if err != nil {
			t.Fatal(err)
		}
		insertGenesis(t, node)
		dn = append(dn, &distNode{id: id, n: node, l: l, port: port})
	}
	parts := make([]string, 0, n)
	for _, d := range dn {
		pub := hex.EncodeToString(d.n.keypair.PublicKeyCompressed())
		parts = append(parts, fmt.Sprintf("%s:%s:%s:1",
			d.id, d.n.nodeAddress, pub))
	}
	spec := strings.Join(parts, ",")
	for _, d := range dn {
		if err := d.n.ConfigureGenesisValidators(spec); err != nil {
			t.Fatal(err)
		}
	}
	return dn
}

// launchCluster 按 launch 標誌啟動節點：掛 P2P、提供 HTTP、進入分佈式共識。
func launchCluster(t *testing.T, dn []*distNode, launch []bool) {
	t.Helper()
	firstURL := ""
	for i, d := range dn {
		if !launch[i] {
			continue
		}
		url := fmt.Sprintf("http://127.0.0.1:%d", d.port)
		d.url = url
		var boot []string
		if firstURL != "" {
			boot = []string{firstURL}
		}
		netw, err := d.n.AttachP2P(url, boot, false)
		if err != nil {
			t.Fatal(err)
		}
		root := http.NewServeMux()
		root.Handle("/p2p/", netw.Handler())
		root.Handle("/", NewRPCServer(d.n).Handler())
		srv := &http.Server{Handler: root}
		go func() { _ = srv.Serve(d.l) }()
		netw.Start(context.Background())
		d.n.StartDistributed()
		d.netw, d.srv, d.launched = netw, srv, true
		if firstURL == "" {
			firstURL = url
		}
	}
	t.Cleanup(func() {
		for _, d := range dn {
			if d.launched {
				d.netw.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = d.srv.Shutdown(ctx)
				cancel()
				_ = d.n.Close()
			} else {
				_ = d.l.Close()
				_ = d.n.Close()
			}
		}
	})
}

func waitFinalized(t *testing.T, n *Node, h int64, to time.Duration) {
	t.Helper()
	deadline := time.Now().Add(to)
	for time.Now().Before(deadline) {
		if n.FinalizedHeight() >= h {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("節點未最終化到 %d：finalized=%d tip=%d",
		h, n.FinalizedHeight(), n.DB().GetTipHeight())
}

// 正常情況：4 驗證人跨節點聚合，全部在同一高度最終化且區塊哈希一致。
func TestDistributedBFTQuorum(t *testing.T) {
	dn := buildCluster(t, 4)
	launchCluster(t, dn, []bool{true, true, true, true})

	for _, d := range dn {
		waitFinalized(t, d.n, 3, 90*time.Second)
	}
	ref, _ := dn[0].n.DB().GetBlock(3)
	if ref == nil {
		t.Fatal("缺少高度3")
	}
	for i := 1; i < 4; i++ {
		b, _ := dn[i].n.DB().GetBlock(3)
		if b == nil || b.Hash != ref.Hash {
			t.Errorf("v%d 高度3 哈希不一致", i)
		}
	}
}

// 容錯 f=1：v3 離線（不投票），其餘 3 人 power3 > quorum(2.67)，
// 在非 v3 提議的高度（h1=v1、h2=v2）仍可跨節點最終化。
func TestDistributedBFTQuorumOneOffline(t *testing.T) {
	dn := buildCluster(t, 4)
	launchCluster(t, dn, []bool{true, true, true, false})

	for i := 0; i < 3; i++ {
		waitFinalized(t, dn[i].n, 2, 90*time.Second)
	}
}

// 非驗證人簽名的投票必須被拒絕。
func TestNonValidatorVoteRejected(t *testing.T) {
	dn := buildCluster(t, 4)
	launchCluster(t, dn, []bool{false, false, false, false})

	kp, _ := crypto.GenerateKeyPair()
	bh := strings.Repeat("a", 64)
	vote, err := bft.SignVote(kp, bft.Precommit, 1, 0, &bh)
	if err != nil {
		t.Fatal(err)
	}
	if err := dn[0].n.handleIncomingVote(vote); err == nil {
		t.Fatal("非驗證人投票應被拒絕")
	}
}

// view change：h3 的 round0 proposer 恰為離線的 v3（3%4=3）；其餘 3 人
// 在本地超時後進 round1，v0（(3+1)%4=0）接棒出塊，集群跨過離線節點
// 最終化 h3 且哈希一致；h4（v0 round0）繼續正常推進。
func TestDistributedBFTViewChangeOfflineProposer(t *testing.T) {
	dn := buildCluster(t, 4)
	launchCluster(t, dn, []bool{true, true, true, false})

	for i := 0; i < 3; i++ {
		waitFinalized(t, dn[i].n, 3, 90*time.Second)
	}
	ref, _ := dn[0].n.DB().GetBlock(3)
	if ref == nil {
		t.Fatal("缺少高度3")
	}
	for i := 1; i < 3; i++ {
		b, _ := dn[i].n.DB().GetBlock(3)
		if b == nil || b.Hash != ref.Hash {
			t.Errorf("v%d 高度3 哈希不一致（view change 後分叉）", i)
		}
	}
}
