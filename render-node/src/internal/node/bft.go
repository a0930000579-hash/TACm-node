package node

import (
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"tacm/internal/consensus/bft"
	"tacm/internal/crypto"
)

const bftTick = 300 * time.Millisecond

// viewChangeTicket 為一條 view-change 認證票：提議某高度切到更高輪次。
type viewChangeTicket struct {
	round int32
	sig   string
}

// CanonicalViewChange 返回 view-change 的簽名規範字符串（唯一、防串改）。
func CanonicalViewChange(height int64, round int32) []byte {
	return []byte(fmt.Sprintf("tacm-viewchange/%d/%d", height, round))
}

// SignViewChange 以本節點密鑰對 view-change 提議簽名。
func (n *Node) SignViewChange(height int64, round int32) (string, error) {
	sig, err := n.keypair.Sign(CanonicalViewChange(height, round))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sig), nil
}

// verifyViewChange 驗證 view-change 票的驗證人身份與簽名。
func (n *Node) verifyViewChange(height int64, round int32, validator, sigHex string) error {
	val := n.vset.Get(validator)
	if val == nil {
		return fmt.Errorf("view-change 簽名者不在驗證人集: %s", validator)
	}
	pubBytes, err := hex.DecodeString(val.PubkeyHex)
	if err != nil {
		return fmt.Errorf("view-change 驗證人公鑰非法: %v", err)
	}
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("view-change 簽名非法: %v", err)
	}
	if !crypto.VerifyWithPublicKey(pubBytes, CanonicalViewChange(height, round), sigBytes) {
		return fmt.Errorf("view-change 簽名驗證失敗: %s h=%d r=%d", validator, height, round)
	}
	return nil
}

// initBFT 以本節點密鑰構造單機驗證人集與誠實 BFT 節點。
func (n *Node) initBFT() error {
	pubhex := hex.EncodeToString(n.keypair.PublicKeyCompressed())
	val, err := bft.NewValidator(n.nodeAddress, pubhex, 1)
	if err != nil {
		return err
	}
	vset, err := bft.NewValidatorSet([]bft.Validator{*val})
	if err != nil {
		return err
	}
	bn, err := bft.NewBFTNode(n.keypair, vset, bft.BehaviorHonest)
	if err != nil {
		return err
	}
	n.vset = vset
	n.bftNode = bn
	n.finalityLog = bft.NewFinalityLog()
	n.precommitVotes = make(map[int64]map[string]bft.Vote)
	return nil
}

// ReplaceValidatorSet 在 Start 前更換驗證人集（多用於測試/多驗證人配置）。
func (n *Node) ReplaceValidatorSet(vset *bft.ValidatorSet) error {
	bn, err := bft.NewBFTNode(n.keypair, vset, bft.BehaviorHonest)
	if err != nil {
		return err
	}
	n.vset = vset
	n.bftNode = bn
	return nil
}

// onNewBlock 本節點接受某高度塊後啟動 BFT（若尚未啟動該高度）。
// 已在該高度時仍調用 ReceiveProposal（冪等：僅 propose 步生效），
// 以支持 view change 後提案晚到的場景。
func (n *Node) onNewBlock(height int64, hash string) {
	if height <= n.FinalizedHeight() {
		return
	}
	if h, _, _ := n.bftNode.RoundInfo(); h == height {
		n.bftNode.ReceiveProposal(&hash, true)
		return
	}
	n.bftNode.StartHeight(height)
	n.bftNode.ReceiveProposal(&hash, true)
}

// handleIncomingViewChange 接收網絡 view-change 票（M12 硬化）：驗證身份與簽名後
// 記入票集；僅當對某輪次的認證權重 ≥ 2/3 多數（QuorumPower）時才切換輪次，
// 防止單節點或少數節點隨意篡改輪次。
func (n *Node) handleIncomingViewChange(height int64, round int32, validator, sigHex string) error {
	if height <= n.FinalizedHeight() {
		return nil
	}
	if n.db.GetTipHeight() >= height {
		return nil // 提案已到，忽略過期 view-change
	}
	if !n.isDistributed() {
		return nil
	}
	if err := n.verifyViewChange(height, round, validator, sigHex); err != nil {
		return err
	}
	switched, err := n.recordViewChangeTicket(height, round, validator, sigHex)
	if err != nil {
		return err
	}
	if switched {
		n.wakeConsensus()
	}
	return nil
}

// viewChangeTickets 為各高度的 view-change 認證票集（驗證人 → 其最高輪次票）。
// 每驗證人每高度僅保留最高 round 一票（更高 round 覆蓋舊票）。
type viewChangeTickets struct {
	mu      sync.Mutex
	byH     map[int64]map[string]viewChangeTicket
	applied map[int64]int32 // 已切換到的 round（防重複切換）
}

func (n *Node) newViewChangeTickets() *viewChangeTickets {
	return &viewChangeTickets{
		byH:     map[int64]map[string]viewChangeTicket{},
		applied: map[int64]int32{},
	}
}

// recordViewChangeTicket 記入一張已驗證的 view-change 票；返回是否發生輪次切換。
func (n *Node) recordViewChangeTicket(height int64, round int32, validator, sigHex string) (bool, error) {
	n.vcMu.Lock()
	defer n.vcMu.Unlock()
	if n.viewChangeTk == nil {
		n.viewChangeTk = n.newViewChangeTickets()
	}
	// 已切到 ≥ round 或更高，忽略。
	if cur, ok := n.viewChangeTk.applied[height]; ok && cur >= round {
		return false, nil
	}

	m, ok := n.viewChangeTk.byH[height]
	if !ok {
		m = map[string]viewChangeTicket{}
		n.viewChangeTk.byH[height] = m
	}
	if prev, ok := m[validator]; ok && prev.round >= round {
		return false, nil // 舊票或同 round 重發
	}
	m[validator] = viewChangeTicket{round: round, sig: sigHex}

	// 統計：聲稱 ≥ 某輪次的驗證人權重。取所有票中的最高輪次檢查是否達多數。
	maxRound := int32(0)
	for _, t := range m {
		if t.round > maxRound {
			maxRound = t.round
		}
	}
	if maxRound <= 0 {
		return false, nil
	}
	power := 0
	for addr, t := range m {
		if t.round >= maxRound {
			if val := n.vset.Get(addr); val != nil {
				power += val.Power
			}
		}
	}
	if !n.vset.HasQuorum(power) {
		return false, nil
	}

	// 多數認證通過：切換到最高認證輪次。
	h, curRound, step := n.bftNode.RoundInfo()
	if h != height {
		n.bftNode.StartHeight(height)
		h, curRound, _ = n.bftNode.RoundInfo()
	}
	if step == bft.StepCommitted {
		return false, nil
	}
	if maxRound > curRound {
		n.bftNode.MoveToRound(maxRound)
	}
	n.viewChangeTk.applied[height] = maxRound
	return true, nil
}

// proposeViewChange 本地超時：本節點簽名並廣播 view-change 票（提議 round+1），
// 自身票也計入；等待多數認證後才真正切換輪次。
func (n *Node) proposeViewChange(height int64) {
	h, curRound, _ := n.bftNode.RoundInfo()
	if h != height {
		n.bftNode.StartHeight(height)
		_, curRound, _ = n.bftNode.RoundInfo()
	}
	newRound := curRound + 1
	sig, err := n.SignViewChange(height, newRound)
	if err != nil {
		return
	}
	if switched, err := n.recordViewChangeTicket(height, newRound, n.nodeAddress, sig); err == nil && switched {
		n.wakeConsensus()
	}
	if n.p2pNet != nil {
		n.p2pNet.BroadcastViewChange(height, newRound, n.nodeAddress, sig)
	}
	log.Printf("[viewchange] %s 提議 h=%d r=%d 超時轉換", n.nodeID, height, newRound)
}

// runBFT 持續推進 BFT：產生投票、檢查最終化。
func (n *Node) runBFT() {
	defer n.wg.Done()
	ticker := time.NewTicker(bftTick)
	defer ticker.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-ticker.C:
			n.bftStep()
		}
	}
}

func (n *Node) bftStep() {
	votes := n.bftNode.Advance()
	for _, v := range votes {
		if v.Type == bft.Precommit {
			n.recordPrecommit(v)
		}
		// 分佈式：把本節點投票廣播給其他驗證人。
		if n.p2pNet != nil {
			n.p2pNet.BroadcastVote(v)
		}
	}
	if !n.bftNode.Committed() {
		return
	}
	h, _, _ := n.bftNode.RoundInfo()
	ch := n.bftNode.CommittedHash()

	n.mu.Lock()
	if h > n.finalizedH {
		n.finalizedH = h
	}
	n.mu.Unlock()

	if ch != nil {
		n.finalityLog.Record(bft.HeightResult{
			Height: h, Committed: true, FinalizedHash: ch,
		})
	}
}

// handleIncomingVote 接收網絡傳來的共識投票：驗簽入狀態機，
// precommit 另記存以供最終性證明/SPV。
func (n *Node) handleIncomingVote(v *bft.Vote) error {
	// M87：雙簽罰沒偵測（equivocation）——同驗證人同高度同輪對不同塊投票 → jail + slash。
	if n.slashTracker != nil {
		if ev := n.slashTracker.Observe(v); ev != nil {
			n.applySlashing(ev)
		}
	}
	if err := n.bftNode.ReceiveVote(v); err != nil {
		return err
	}
	if v.Type == bft.Precommit {
		n.recordPrecommit(v)
	}
	return nil
}

func (n *Node) recordPrecommit(v *bft.Vote) {
	n.precommitMu.Lock()
	defer n.precommitMu.Unlock()
	m, ok := n.precommitVotes[v.Height]
	if !ok {
		m = make(map[string]bft.Vote)
		n.precommitVotes[v.Height] = m
	}
	m[v.Validator] = *v
}

// FinalizedHeight 返回已 BFT 最終化的高度。
func (n *Node) FinalizedHeight() int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.finalizedH
}

// ValidatorSet 返回驗證人集。
func (n *Node) ValidatorSet() *bft.ValidatorSet { return n.vset }

// FinalityInfo 為最終性狀態。
type FinalityInfo struct {
	FinalizedHeight int64 `json:"finalized_height"`
	TipHeight       int64 `json:"tip_height"`
	ValidatorCount  int   `json:"validator_count"`
	TotalPower      int   `json:"total_power"`
	QuorumPower     int   `json:"quorum_power"`
	HasBFT          bool  `json:"has_bft"`
}

// GetFinalityInfo 返回最終性概況。
func (n *Node) GetFinalityInfo() FinalityInfo {
	return FinalityInfo{
		FinalizedHeight: n.FinalizedHeight(),
		TipHeight:       n.db.GetTipHeight(),
		ValidatorCount:  n.vset.Len(),
		TotalPower:      n.vset.TotalPower(),
		QuorumPower:     n.vset.QuorumPower(),
		HasBFT:          n.bftNode != nil,
	}
}

// FinalityProof 為某高度的最終性證明（precommit 投票集）。
type FinalityProof struct {
	Height      int64      `json:"height"`
	BlockHash   string     `json:"block_hash"`
	Votes       []bft.Vote `json:"votes"`
	VotePower   int        `json:"vote_power"`
	QuorumPower int        `json:"quorum_power"`
	Finalized   bool       `json:"finalized"`
}

// GetFinalityProof 返回某高度的 precommit 最終性證明。
func (n *Node) GetFinalityProof(height int64) *FinalityProof {
	n.precommitMu.Lock()
	m := n.precommitVotes[height]
	votes := make([]bft.Vote, 0, len(m))
	power := 0
	for _, v := range m {
		votes = append(votes, v)
		if val := n.vset.Get(v.Validator); val != nil {
			power += val.Power
		}
	}
	n.precommitMu.Unlock()

	blockHash := ""
	if b, err := n.db.GetBlock(height); err == nil && b != nil {
		blockHash = b.Hash
	}
	return &FinalityProof{
		Height: height, BlockHash: blockHash, Votes: votes,
		VotePower: power, QuorumPower: n.vset.QuorumPower(),
		Finalized: n.vset.HasQuorum(power),
	}
}
