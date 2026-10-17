package node

import (
	"fmt"
	"math"
	"time"

	"tacm/internal/consensus/bft"
	"tacm/internal/consensus/pos"
)

// GetPosStore 回傳 PoS 質押層（可能為 nil：pos.db 未開啟）。
func (n *Node) GetPosStore() *pos.Store { return n.posStore }

// powerPerStake 每個投票權重對應的質押量（TACM）：權重 = ceil(total_stake / powerPerStake)。
// 避免權重過大拖慢 BFT 法定人數計算，也避免過小失去區分度。
const powerPerStake = 100.0

// RebuildValidatorSetFromPOS 以 PoS 質押層重建 BFT 驗證人集（M87 動態驗證人集）：
//   - 取質押最高前 ValidatorCount（21）名「活躍且質押 > 0」的驗證人；
//   - 權重 = ceil(total_stake / powerPerStake)（每 100 TACM = 1 power）；
//   - 驗證人地址須與其註冊公鑰派生一致（bft.NewValidator 校驗）。
//
// 若 PoS 層無可用驗證人（pos 未開啟／空集），保持既有驗證人集不變（回退安全）。
func (n *Node) RebuildValidatorSetFromPOS() error {
	if n.posStore == nil {
		return nil // pos 未開啟：維持創世集
	}
	vals, err := n.posStore.GetActiveValidatorsForConsensus(pos.DefaultConfig().ValidatorCount)
	if err != nil {
		return fmt.Errorf("node: 讀取 PoS 驗證人失敗: %w", err)
	}
	if len(vals) == 0 {
		return nil // 尚無質押驗證人：維持創世集
	}
	bv := make([]bft.Validator, 0, len(vals))
	for i := range vals {
		v := &vals[i]
		if v.TotalStake <= 0 || v.Status != "active" {
			continue
		}
		power := int(math.Ceil(v.TotalStake / powerPerStake))
		if power < 1 {
			power = 1
		}
		val, err := bft.NewValidator(v.Address, v.PubkeyHex, power)
		if err != nil {
			continue
		}
		bv = append(bv, *val)
	}
	if len(bv) == 0 {
		return nil
	}
	vset, err := bft.NewValidatorSet(bv)
	if err != nil {
		return fmt.Errorf("node: 動態驗證人集非法: %w", err)
	}
	if err := n.ReplaceValidatorSet(vset); err != nil {
		return fmt.Errorf("node: 更換驗證人集失敗: %w", err)
	}
	return nil
}

// StartDynamicValidators 啟動動態驗證人集週期重建（每 60 秒一次，背景 goroutine；
// 直到 stopCh 關閉）。質押／註冊變動後最遲 1 分鐘生效，符合主流鏈 epoch 式驗證人輪換語義。
func (n *Node) StartDynamicValidators() {
	if n.posStore == nil {
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		tk := time.NewTicker(60 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-n.stopCh:
				return
			case <-tk.C:
				if err := n.RebuildValidatorSetFromPOS(); err != nil {
					// 週期重建失敗不致命：下次 tick 重試
				}
			}
		}
	}()
}

// applySlashing 對雙重簽名驗證人執行罰沒：監禁（jail）＋按 SlashRate 扣減質押，
// 並立即重建驗證人集。pos 未開啟時僅記錄（不影響共識）。
func (n *Node) applySlashing(ev *bft.SlashingEvidence) {
	if n.posStore == nil || ev == nil {
		return
	}
	if _, err := n.posStore.JailValidator(ev.Validator, "equivocation "+ev.VoteType+" h="+fmt.Sprint(ev.Height)); err != nil {
		return
	}
	if _, err := n.posStore.SlashValidator(ev.Validator, pos.DefaultConfig().SlashRate, "equivocation double-vote"); err != nil {
		return
	}
	_ = n.RebuildValidatorSetFromPOS()
}
