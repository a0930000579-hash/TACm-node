package node

// v165 重啟恢復 e2e：節點重啟（Suspend→Resume）時，ResumeConsensus 必須從持久化層
// （鏈上 pos_validators）完整還原動態驗證人集——pubkey、自質押+委託量與排名、jail 狀態、
// slash 累積——不得漏載、不得回退到創世靜態集合；領導者與 follower 路徑重建結果一致。
//
// 場景：
//  1. 建 3 驗證人叢集（v0/v1/v2），對每個節點寫入同一條鏈的同步視圖：
//     v0=2000、v1=4000、v2=3000（自質押＋累計模擬委託併入 total_stake）；
//  2. v2 監禁（jail）、v1 罰沒 slash 50%（4000→2000）——模擬動態退出/懲罰；
//  3. 重啟前在 v0 上重建一次 vset，材料化「已知正確」快照；
//  4. follower（v1）以同份鏈視圖重建，必須與 v0 完全相同（領導者/follower 一致）；
//  5. 模擬程序重開：SuspendConsensus 後把記憶體 vset 重設回 New().initBFT 的創世單節點集，
//     強迫 ResumeConsensus 從鏈上持久化還原（而非沿用記憶體殘留）；
//  6. ResumeConsensus 後斷言 vset（address/pubkey/power/排名）與重啟前完全一致、
//     v1 已還原入集（未回退創世集）、v2 仍為 jail 排除。

import (
	"encoding/hex"
	"fmt"
	"sort"
	"testing"
	"time"

	"tacm/internal/consensus/bft"
)

// vsetSnapshot 把 vset 攤平為可比較的有序序列（address|pubkey|power）。
// 排序消除枚舉順序差異，僅比對集合內容與權重（排名）。
func vsetSnapshot(v *bft.ValidatorSet) []string {
	if v == nil {
		return nil
	}
	out := make([]string, 0, v.Len())
	for _, val := range v.List() {
		out = append(out, fmt.Sprintf("%s|%s|%d", val.Address, val.PubkeyHex, val.Power))
	}
	sort.Strings(out)
	return out
}

func equalVSetSnapshot(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResumeConsensus_RestoresDynamicValidators(t *testing.T) {
	dn := buildCluster(t, 3) // v0,v1,v2（各自密鑰、創世；尚未 launch）
	now := time.Now().Unix()

	// 三個驗證人不同質押量（slash 前排名 v1=4000 > v2=3000 > v0=2000）。
	stakes := map[string]float64{
		dn[0].n.nodeAddress: 2000,
		dn[1].n.nodeAddress: 4000,
		dn[2].n.nodeAddress: 3000,
	}
	// 對每個節點寫入全部質押者（模擬同一條鏈同步後的一致視圖）。
	for _, d := range dn {
		for _, s := range dn {
			pub := hex.EncodeToString(s.n.keypair.PublicKeyCompressed())
			if err := d.n.DB().ApplyPosStake(s.n.nodeAddress, pub, stakes[s.n.nodeAddress], now); err != nil {
				t.Fatal(err)
			}
		}
	}

	// v2 監禁（jail）→ 不應進入驗證人集；v1 罰沒 50%（4000→2000，仍 active）。
	for _, d := range dn {
		if err := d.n.DB().JailPosValidator(dn[2].n.nodeAddress); err != nil {
			t.Fatal(err)
		}
		if err := d.n.DB().SlashPosValidator(dn[1].n.nodeAddress, 0.5); err != nil {
			t.Fatal(err)
		}
	}

	// —— 重啟前：在 v0 上重建一次，材料化「已知正確」vset。——
	if err := dn[0].n.RebuildValidatorSetFromPOS(); err != nil {
		t.Fatal(err)
	}
	before := vsetSnapshot(dn[0].n.vset)

	// 重啟前斷言集合內容。
	if dn[0].n.vset.Get(dn[0].n.nodeAddress) == nil {
		t.Fatal("v0 應在驗證人集")
	}
	if dn[0].n.vset.Get(dn[1].n.nodeAddress) == nil {
		t.Fatal("v1（slash 後仍 active）應在驗證人集")
	}
	if got := dn[0].n.vset.Get(dn[2].n.nodeAddress); got != nil {
		t.Fatalf("v2 已 jail，不應在驗證人集，實得 %+v", got)
	}
	// pubkey 正確性：v0 集內公鑰必須等於其密鑰派生的壓縮公鑰。
	if v0v := dn[0].n.vset.Get(dn[0].n.nodeAddress); v0v != nil {
		wantPub := hex.EncodeToString(dn[0].n.keypair.PublicKeyCompressed())
		if v0v.PubkeyHex != wantPub {
			t.Fatalf("v0 pubkey 不符：got %s want %s", v0v.PubkeyHex, wantPub)
		}
	}

	// follower 路徑一致性：v1 以同一份鏈視圖重建，必須得到與 v0 完全相同的 vset。
	if err := dn[1].n.RebuildValidatorSetFromPOS(); err != nil {
		t.Fatal(err)
	}
	if !equalVSetSnapshot(before, vsetSnapshot(dn[1].n.vset)) {
		t.Fatalf("領導者/follower 重建結果不一致：\n v0=%v\n v1=%v",
			before, vsetSnapshot(dn[1].n.vset))
	}

	// —— 模擬節點重啟 ——
	dn[0].n.SuspendConsensus()
	// 程序重開 New().initBFT 只建創世單節點 vset：把記憶體 vset 重設回創世狀態，
	// 強迫 ResumeConsensus 從鏈上持久化還原（而非沿用記憶體殘留）。
	if err := dn[0].n.initBFT(); err != nil {
		t.Fatal(err)
	}
	if dn[0].n.vset.Len() != 1 || dn[0].n.vset.Get(dn[1].n.nodeAddress) != nil {
		t.Fatalf("前置條件失敗：重設後應為創世單節點集，實得 len=%d", dn[0].n.vset.Len())
	}

	// 恢復：ResumeConsensus 應自動從鏈上還原完整動態驗證人集。
	dn[0].n.ResumeConsensus()
	defer dn[0].n.SuspendConsensus() // 收尾停掉 Resume 起的 goroutine（Cleanup 再 Close）

	after := vsetSnapshot(dn[0].n.vset)
	if !equalVSetSnapshot(before, after) {
		t.Fatalf("重啟後驗證人集未從持久化還原：\n before=%v\n after=%v", before, after)
	}
	// 不回退創世靜態集合：還原後應含 v1（創世集只有 v0 自己）。
	if dn[0].n.vset.Get(dn[1].n.nodeAddress) == nil {
		t.Fatal("重啟後 v1 應已從鏈上還原入集（不得回退創世單節點集）")
	}
	if got := dn[0].n.vset.Get(dn[2].n.nodeAddress); got != nil {
		t.Fatalf("重啟後 v2 仍應為 jail 排除狀態，實得 %+v", got)
	}
	t.Logf("重啟恢復成功：vset=%v", after)
}
