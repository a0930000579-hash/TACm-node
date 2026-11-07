
package chaindb

import (
	"fmt"
	"testing"
	"time"
)

// TestPosBootstrap 驗證節點引導獎勵：核發 1,000＋自動質押＋限額/單地址防重複。
func TestPosBootstrap(t *testing.T) {
	c := openTmpDB(t)
	defer c.Close()
	// 創世社群基金（memo=genesis:community_pool 入賬 100,000 TACm）。
	pool := "tx0CommunityPool0000000000000000000000000001"
	genesis := &Block{Height: 0, Hash: "0"}
	gtxs := []Transaction{{
		TxHash: "genesis-community-pool", ToAddr: pool, Amount: "100000",
		Fee: "0", Ts: time.Now().Unix(), Memo: "genesis:community_pool", Status: "confirmed",
	}}
	if err := c.InsertBlock(genesis, gtxs); err != nil {
		t.Fatalf("寫創世塊: %v", err)
	}

	nodeA := "tx0NodeA000000000000000000000000000000000001"
	b1 := &Block{Height: 1, Hash: "1"}
	txs := []Transaction{{
		TxHash: "boot-a", FromAddr: nodeA, ToAddr: "pos_stake_pool",
		Amount: "0", Fee: "0", Ts: time.Now().Unix(), Memo: "pos:bootstrap", Status: "confirmed",
	}}
	if err := c.InsertBlock(b1, txs); err != nil {
		t.Fatalf("節點A引導: %v", err)
	}
	var balF float64
	_, _ = fmt.Sscanf(c.GetBalance(nodeA), "%g", &balF)
	if balF != 1000 {
		t.Errorf("節點A應得 1,000，實得 %s", c.GetBalance(nodeA))
	}
	pvs, err := c.GetPosValidators()
	if err != nil || len(pvs) != 1 || pvs[0].TotalStake != 1000 {
		t.Errorf("質押累計錯誤: %+v err=%v", pvs, err)
	}
	// 重複領取（同地址）應失敗。
	b2 := &Block{Height: 2, Hash: "2"}
	txs2 := []Transaction{{
		TxHash: "boot-a2", FromAddr: nodeA, ToAddr: "pos_stake_pool",
		Amount: "0", Fee: "0", Ts: time.Now().Unix(), Memo: "pos:bootstrap", Status: "confirmed",
	}}
	if err := c.InsertBlock(b2, txs2); err == nil {
		t.Fatal("重複領取應被拒絕")
	}
	// 限額：填滿至上限，最後一名之外應被拒絕。
	for i := 0; i < PosBootstrapLimit-1; i++ {
		addr := "tx0NodeZ00000000000000000000000000000000000" + string(rune('a'+i))
		hb := &Block{Height: 3 + int64(i), Hash: string(rune('a' + i))}
		htx := []Transaction{{
			TxHash: "boot-z", FromAddr: addr, ToAddr: "pos_stake_pool",
			Amount: "0", Fee: "0", Ts: time.Now().Unix(), Memo: "pos:bootstrap", Status: "confirmed",
		}}
		if err := c.InsertBlock(hb, htx); err != nil {
			t.Fatalf("第%d名引導: %v", i+2, err)
		}
	}
	cnt, _ := c.GetBootstrapCount()
	if cnt != PosBootstrapLimit {
		t.Errorf("限額計數 = %d, want %d", cnt, PosBootstrapLimit)
	}
}
