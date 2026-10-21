package node

// v56：鏈上質押驗證人分布式 BFT 測試——
//  1. 節點鏈上質押（pos_validators active、≥1,000 TACm）→ IsValidator()=true；
//  2. 鏈上質押驗證人以分布式 BFT 輪值出塊；創世節點（v0）掛機時另一驗證人（v1）
//     接管出塊，鏈持續推進（參照比特幣/主流鏈：創世節點掛了區塊鏈仍能運行）；
//  3. 新質押驗證人經 RebuildValidatorSetFromPOS 納入輪值（epoch 式驗證人輪換語義，
//     v56：StartDistributed 已啟動動態驗證人週期重建）。
//
// 注意：質押資料在真實系統中經 pos:stake 交易上鏈、全節點同步同一份鏈上狀態；
// 本測試直接寫 DB 時須對每個節點寫入「全部」質押者，才能模擬同步後的一致視圖。

import (
	"context"
	"encoding/hex"
	"testing"
	"time"
)

// v56StakeAll 對每個節點的鏈上質押表寫入全部節點的質押（模擬同一條鏈的同步視圖）。
func v56StakeAll(t *testing.T, dn []*distNode, amt float64) {
	t.Helper()
	for _, d := range dn {
		for _, s := range dn {
			pub := hex.EncodeToString(s.n.keypair.PublicKeyCompressed())
			if err := d.n.DB().ApplyPosStake(s.n.nodeAddress, pub, amt, time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestValidatorDistributed_ChainStakeRoundRobin v56 e2e：鏈上質押 2 驗證人分布式輪值，
// 創世節點（v0）掛機後 v1 接管，鏈持續推進——創世節點掛了鏈仍運行。
func TestValidatorDistributed_ChainStakeRoundRobin(t *testing.T) {
	dn := buildCluster(t, 2)
	v56StakeAll(t, dn, 2000)
	if !dn[0].n.IsValidator() || !dn[1].n.IsValidator() {
		t.Fatalf("鏈上質押後應為驗證人: v0=%v v1=%v",
			dn[0].n.IsValidator(), dn[1].n.IsValidator())
	}
	// 手動重建一次確保兩者入集（StartDistributed 的週期重建亦會覆蓋）。
	for _, d := range dn {
		if err := d.n.RebuildValidatorSetFromPOS(); err != nil {
			t.Fatal(err)
		}
	}
	launchCluster(t, dn, []bool{true, true})

	// 1. 兩驗證人正常輪值出塊到 h≥3（鏈上質押驗證人網路運作正常）。
	waitTip(t, dn[0].n, 3, 60*time.Second)
	waitTip(t, dn[1].n, 3, 60*time.Second)

	// 2. 創世節點 v0 掛機（真實主鏈進程死亡語義）：HTTP 下線＋暫停共識＋關閉 P2P。
	shutCtx, shutCancel := context.WithTimeout(context.Background(), time.Second)
	_ = dn[0].srv.Shutdown(shutCtx)
	shutCancel()
	dn[0].n.SuspendConsensus()
	dn[0].netw.Close()

	// 3. v1 接管：輪到 v0 的高度由 v1 超時補位，tip 持續推進——創世節點掛了鏈仍運行。
	tipBefore := dn[1].n.DB().GetTipHeight()
	waitTip(t, dn[1].n, tipBefore+3, 60*time.Second)
	t.Logf("創世節點掛機後 v1 接管：tip %d → %d", tipBefore, dn[1].n.DB().GetTipHeight())
}

// TestValidatorDistributed_NewStakeJoinsValidatorSet 新質押驗證人納入輪值：
// RebuildValidatorSetFromPOS 以鏈上質押取代創世集；輪值 proposer 依高度交替
// （epoch 式驗證人輪換語義）。
func TestValidatorDistributed_NewStakeJoinsValidatorSet(t *testing.T) {
	dn := buildCluster(t, 2)
	v56StakeAll(t, dn, 2000)
	if err := dn[0].n.RebuildValidatorSetFromPOS(); err != nil {
		t.Fatal(err)
	}
	// vset 應含 v0、v1（鏈上質押取代創世集）。
	for _, d := range dn {
		if dn[0].n.vset == nil || dn[0].n.vset.Get(d.n.nodeAddress) == nil {
			t.Fatalf("%s 不在重建後驗證人集", d.id)
		}
	}
	// 輪值交替：連續高度 proposer 應覆蓋兩節點。
	seen := map[string]bool{}
	for h := int64(1); h <= 6; h++ {
		p := dn[0].n.vset.Proposer(h, 0)
		if p != nil {
			seen[p.Address] = true
		}
	}
	for _, d := range dn {
		if !seen[d.n.nodeAddress] {
			t.Fatalf("%s 未出現在輪值 proposer 序列", d.id)
		}
	}
}

// TestValidatorDistributed_SingleValidatorStillProduces 單驗證人分布式模式（主鏈
// 目前僅 1 個質押驗證人的狀態）：vset 僅自己、proposer 恆為本節點，出塊照常推進；
// view change 由單人認證即切輪——保證 v56 主鏈升級後行為不退化。
func TestValidatorDistributed_SingleValidatorStillProduces(t *testing.T) {
	dn := buildCluster(t, 1)
	v56StakeAll(t, dn, 2000)
	if !dn[0].n.IsValidator() {
		t.Fatalf("鏈上質押後應為驗證人: %v", dn[0].n.IsValidator())
	}
	if err := dn[0].n.RebuildValidatorSetFromPOS(); err != nil {
		t.Fatal(err)
	}
	launchCluster(t, dn, []bool{true})
	waitTip(t, dn[0].n, 3, 60*time.Second)
	t.Logf("單驗證人分布式出塊正常：tip=%d", dn[0].n.DB().GetTipHeight())
}
