package node

// heartbeat_v51_test.go — v51：PoT 節點註冊（node:reg）上鏈與冪等重試驗證。
// 節點啟動後自動註冊公鑰；未註冊時每個心跳週期重試直到成功（不再只試一次）。

import (
	"testing"
	"time"
)

// TestRegisterAsPotNode：RegisterAsPotNode 提交 node:reg 交易並入塊，
// 鏈上 NodeRegistry 含節點地址；重複註冊（冪等）不報錯且不產生新 nonce 衝突。
func TestRegisterAsPotNode(t *testing.T) {
	cfg := testCfg(t)
	n, err := New(cfg, "node1", 1)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := n.keypair.Address()
	if err != nil {
		t.Fatal(err)
	}
	insertGenesisWithAlloc(t, n, admin)
	n.Start()
	defer func() { _ = n.Close() }()

	// 初始：鏈上無註冊。
	regs, err := n.db.GetNodeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("初始 NodeRegistry 應為空: %d 筆", len(regs))
	}

	// 註冊 → 交易入塊 → 鏈上出現本節點。
	hash, err := n.RegisterAsPotNode()
	if err != nil {
		t.Fatalf("RegisterAsPotNode error: %v", err)
	}
	if hash == "" {
		t.Fatal("hash 不應為空")
	}
	waitFor(t, func() bool {
		rs, err := n.db.GetNodeRegistry()
		if err != nil {
			return false
		}
		for i := range rs {
			if rs[i].Address == admin {
				return true
			}
		}
		return false
	}, 5*time.Second, "node:reg 應在 5s 內入塊並註冊")

	// 冪等：已註冊再呼叫不報錯（鏈上 upsert）。
	if _, err := n.RegisterAsPotNode(); err != nil {
		t.Fatalf("重複 RegisterAsPotNode 應冪等成功: %v", err)
	}
}
