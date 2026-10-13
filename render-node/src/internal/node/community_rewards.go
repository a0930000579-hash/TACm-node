package node

// community_rewards.go — M73-B 社群鏈上激勵：
//  1. 互動積分（發文/按讚/留言/市集成交/廣告）由 community.Store 累積；
//  2. 每 60 塊結算一期：按積分比例，由「社群基金」（節點派生金鑰地址，
//     創世分配 100,000 TACm）簽名發放鏈上 TACm，memo=`community:reward:P<n>`，
//     進 mempool 出塊上鏈——全鏈可審計、簽名有效、餘額守恆。

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/community"
	"tacm/internal/crypto"
)

const (
	// CommunityPoolLabel 社群基金派生標籤（DeriveKey(nodeSeed, label)）。
	CommunityPoolLabel = "community_pool"

	// CommunityRewardInterval 結算週期（區塊數）：每 60 塊結算一期。
	CommunityRewardInterval int64 = 60

	// CommunityRewardPerPeriod 每期獎勵池（TACm，由社群基金提撥）。
	CommunityRewardPerPeriod = 50.0

	// 互動積分權重。
	RewardPtsPost    int64 = 5  // 發文
	RewardPtsLike    int64 = 1  // 按讚
	RewardPtsComment int64 = 3  // 留言
	RewardPtsSold    int64 = 10 // 市集成交（買方）
	RewardPtsAdPaid  int64 = 5  // 廣告付費（投放者）
)

// communityPoolKeypair 取得社群基金金鑰（節點種子派生，確定性、持久）。
func (n *Node) communityPoolKeypair() (*crypto.KeyPair, error) {
	if n.keypair == nil {
		return nil, fmt.Errorf("node key unavailable")
	}
	return crypto.DeriveKey(n.keypair.PrivateKey(), CommunityPoolLabel)
}

// CommunityPoolAddress 回傳社群基金鏈上地址。
func (n *Node) CommunityPoolAddress() (string, error) {
	kp, err := n.communityPoolKeypair()
	if err != nil {
		return "", err
	}
	return kp.Address()
}

// SettleCommunityRewards 結算一期社群獎勵（tip 距上次 ≥60 塊才執行）。
// 返回已結算的獲獎人數；未達週期或無積分返回 0（非錯誤）。
func (n *Node) SettleCommunityRewards(tip int64) (int, error) {
	if n.communitySvc == nil {
		return 0, fmt.Errorf("社群未啟用")
	}
	kp, err := n.communityPoolKeypair()
	if err != nil {
		return 0, err
	}
	from, err := kp.Address()
	if err != nil {
		return 0, err
	}
	meta, err := n.communitySvc.GetRewardMeta()
	if err != nil {
		return 0, err
	}
	if tip < meta.LastTip+CommunityRewardInterval {
		return 0, nil // 未到結算期
	}
	rows, err := n.communitySvc.SnapshotAndClearRewards()
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		// 無積分也推進期號/鏈高，避免每塊重掃。
		if err := n.communitySvc.SetRewardMeta(meta.Period+1, tip); err != nil {
			return 0, err
		}
		return 0, nil
	}
	var sum int64
	for _, r := range rows {
		sum += r.Points
	}
	period := meta.Period + 1
	ts := time.Now().Unix()
	nonce := n.db.GetNonce(from)
	settled := 0
	remainder := CommunityRewardPerPeriod
	lastIdx := len(rows) - 1
	for i, r := range rows {
		var amt float64
		if i == lastIdx {
			amt = remainder // 末位吃尾差，保證期總額精確 = PerPeriod
		} else {
			amt = CommunityRewardPerPeriod * float64(r.Points) / float64(sum)
		}
		if amt < 1e-6 {
			continue
		}
		remainder -= amt
		tx := map[string]any{
			"from": from, "to": r.Address,
			"amount": chaindb.FormatFloat(amt), "fee": "0",
			"nonce": nonce, "ts": ts,
			"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
			"memo":   fmt.Sprintf("community:reward:P%d", period),
		}
		sig, serr := crypto.SignTransaction(tx, kp.PrivateKey())
		if serr != nil {
			return settled, fmt.Errorf("社群獎勵簽名失敗: %w", serr)
		}
		tx["signature"] = sig
		if _, serr := n.SubmitTransaction(tx); serr != nil {
			// 單筆失敗（如該地址無效）不影響整期；記錄並繼續。
			_ = serr
			continue
		}
		nonce++
		settled++
	}
	if err := n.communitySvc.SetRewardMeta(period, tip); err != nil {
		return settled, err
	}
	return settled, nil
}

// TryCommunityRewardSettle 惰性結算入口：任何社群互動/查詢時呼叫，
// 鏈高達標即結算（自動上鏈）；返回是否本輪執行了結算。
func (n *Node) TryCommunityRewardSettle() bool {
	if n.communitySvc == nil || n.db == nil {
		return false
	}
	tip := n.db.GetTipHeight()
	if tip <= 0 {
		return false
	}
	meta, err := n.communitySvc.GetRewardMeta()
	if err != nil {
		return false
	}
	if tip < meta.LastTip+CommunityRewardInterval {
		return false
	}
	nSettled, err := n.SettleCommunityRewards(tip)
	if err != nil {
		return false
	}
	return nSettled > 0
}

// rewardPoints 依互動類型給積分（RPC 層呼叫）。
func rewardPoints(kind string) int64 {
	switch kind {
	case "post":
		return RewardPtsPost
	case "like":
		return RewardPtsLike
	case "comment":
		return RewardPtsComment
	case "sold":
		return RewardPtsSold
	case "ad":
		return RewardPtsAdPaid
	}
	return 0
}

// addRewardPoints 給成員累積積分（社群已啟用時）。
func (n *Node) addRewardPoints(address, kind string) {
	if n.communitySvc == nil || address == "" {
		return
	}
	if pts := rewardPoints(kind); pts > 0 {
		_ = n.communitySvc.AddPoints(address, pts)
	}
}

// parseTACM 轉 TACm 小數字串為 float64（結算/展示共用）。
func parseTACM(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

var _ = community.RewardRow{} // 確保 import 保留（型別共用）。
