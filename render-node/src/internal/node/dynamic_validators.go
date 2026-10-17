package node

import (
	"fmt"
	"math"
	"sort"
	"time"

	"tacm/internal/chaindb"
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
	// M149m 去中心化：驗證人集來源改為鏈上質押狀態（pos:stake 交易重放累計），
	// 取代本地 pos.db——任何節點同步同一條鏈即得到相同的驗證人集，天然一致。
	vals, err := n.db.GetPosValidators()
	if err != nil {
		return fmt.Errorf("node: 讀取鏈上驗證人失敗: %w", err)
	}
	// M149m-9：確定性鏈上時間（驗證人集重建不得依賴各節點本機時鐘）。
	tip := n.db.GetTipHeight()
	tipTs := time.Now().Unix()
	if blk, err := n.db.GetBlock(tip); err == nil && blk != nil && blk.Ts > 0 {
		tipTs = blk.Ts
	}
	cfg := pos.DefaultConfig()
	minStk := cfg.ValidatorMinStk // 1,000 TACm
	count := cfg.ValidatorCount    // 21
	act := make([]chaindb.PosValidator, 0, len(vals))
	for _, v := range vals {
		if v.Status != "active" || v.TotalStake < minStk {
			continue
		}
		act = append(act, v)
	}
	if len(act) == 0 {
		// M149m-9：無質押者時仍把 PoT 在線節點併入（時間權證出塊），
		// 否則純 PoT 期（尚未有人質押）節點無法出塊。
		return n.mergePotNodes(nil, nil, tipTs)
	}
	// 質押高者優先，取前 count 名（主流 epoch 式驗證人輪換語義）。
	sort.Slice(act, func(i, j int) bool { return act[i].TotalStake > act[j].TotalStake })
	if len(act) > count {
		act = act[:count]
	}
	bv := make([]bft.Validator, 0, len(act))
	for i := range act {
		v := &act[i]
		power := int(math.Ceil(v.TotalStake / powerPerStake))
		if power < 1 {
			power = 1
		}
		// M149m-3 POT 時間權證接入出塊權：在線時長年化加成（最高 +30%），
		// 時間面前人人平等——持續在線的節點即使質押較少也獲得合理權重（白皮書初衷）。
		// M149m-9：改用鏈上 tip 時間（非本機時間），保證全節點重建的驗證人集完全一致。
		if onlineSec := float64(tipTs - v.FirstSeen); onlineSec > 0 {
			timeW := math.Min(1.0, onlineSec/31536000.0)
			if add := int(math.Ceil(float64(power) * timeW * 0.3)); add > 0 {
				power += add
			}
		}
		val, err := bft.NewValidator(v.Address, v.Pubkey, power)
		if err != nil {
			continue
		}
		bv = append(bv, *val)
	}
	if len(bv) == 0 {
		return nil
	}
	return n.mergePotNodes(bv, act, tipTs)
}

// mergePotNodes 把鏈上在線 PoT 節點（node:reg 註冊＋hb 窗口內在線）併入驗證人集，
// 依時間權證（在線時長）給權重：每 potPowerIntervalSec 在線 +1，上限 potPowerCap。
// 確定性：全部輸入來自鏈上（註冊表、心跳交易、tip 時間）。
func (n *Node) mergePotNodes(bv []bft.Validator, act []chaindb.PosValidator, tipTs int64) error {
	staked := make(map[string]bool, len(act))
	for i := range act {
		staked[act[i].Address] = true
	}
	regs, err := n.db.GetNodeRegistry()
	if err != nil {
		return fmt.Errorf("node: 讀 PoT 節點註冊失敗: %w", err)
	}
	byAddr := make(map[string]chaindb.NodeRegistry, len(regs))
	for i := range regs {
		byAddr[regs[i].Address] = regs[i]
	}
	online, err := n.onChainOnlineMiners(hbWindow)
	if err != nil {
		return fmt.Errorf("node: 讀在線節點失敗: %w", err)
	}
	potPowerInterval := int64(6 * 3600) // 每在線 6 小時 +1 權重
	const potPowerCap = 10             // 上限 10（≈1,000 TACm 質押等價），防止純時間節點無限膨脹
	for addr := range online {
		if staked[addr] {
			continue
		}
		reg, ok := byAddr[addr]
		if !ok || reg.Pubkey == "" {
			continue // 未註冊公鑰的地址無法參與 BFT 簽名，跳過
		}
		power := 1 + int((tipTs-reg.FirstSeen)/potPowerInterval)
		if power > potPowerCap {
			power = potPowerCap
		}
		if power < 1 {
			power = 1
		}
		val, err := bft.NewValidator(reg.Address, reg.Pubkey, power)
		if err != nil {
			continue
		}
		bv = append(bv, *val)
	}
	if len(bv) == 0 {
		return nil // 維持現有驗證人集（創世集）
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
	// M149m：鏈上罰沒同步（驗證人集來源已改鏈上，jail/slash 必須反映到鏈上狀態）。
	_ = n.db.JailPosValidator(ev.Validator)
	_ = n.db.SlashPosValidator(ev.Validator, pos.DefaultConfig().SlashRate)
	_ = n.RebuildValidatorSetFromPOS()
}
