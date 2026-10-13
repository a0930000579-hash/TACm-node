package bft

import (
	"sync"
	"time"

	"tacm/internal/crypto"
)

// HeightResult 為某高度共識結果。
type HeightResult struct {
	Height        int64              `json:"height"`
	Committed     bool               `json:"committed"`
	FinalizedHash *string            `json:"finalized_hash"`
	Evidence      []SlashingEvidence `json:"evidence"`
	Slashed       map[string]float64 `json:"slashed"`
	RoundsUsed    int32              `json:"rounds_used"`
}

// BFTCoordinator 在單進程內協調一組 BFTNode 完成某高度共識（用於測試/演示）。
type BFTCoordinator struct {
	nodes     []*BFTNode
	vset      *ValidatorSet
	maxRounds int32
	slashing  *SlashingTracker
	byAddr    map[string]*BFTNode
}

// NewBFTCoordinator 構造協調器。
func NewBFTCoordinator(nodes []*BFTNode, vset *ValidatorSet, maxRounds int32, slashAmount float64) *BFTCoordinator {
	byAddr := make(map[string]*BFTNode, len(nodes))
	for _, n := range nodes {
		byAddr[n.address] = n
	}
	return &BFTCoordinator{
		nodes:     nodes,
		vset:      vset,
		maxRounds: maxRounds,
		slashing:  NewSlashingTracker(slashAmount),
		byAddr:    byAddr,
	}
}

func (c *BFTCoordinator) broadcast(votes []*Vote) {
	for _, v := range votes {
		c.slashing.Observe(v)
		for _, n := range c.nodes {
			_ = n.ReceiveVote(v) // 含自己票，重入冪等
		}
	}
}

// RunHeight 驅動某高度共識，迭代到最終化或用盡輪次（含 view change）。
func (c *BFTCoordinator) RunHeight(height int64, blockHash string, valid bool) HeightResult {
	for _, n := range c.nodes {
		n.StartHeight(height)
	}

	committed := false
	var finalized *string

	for rnd := int32(0); rnd <= c.maxRounds; rnd++ {
		// 1) Proposer 提議
		prop := c.vset.Proposer(height, rnd)
		pnode := c.byAddr[prop.Address]
		beh := BehaviorOffline
		if pnode != nil {
			beh = pnode.behavior
		}
		var proposal *string
		pvalid := false
		hasProposal := false
		switch beh {
		case BehaviorOffline:
			// 無提議，觸發 view change
		case BehaviorWrongBlock:
			proposal = ptrString(rep("0badc0de", 8))
			pvalid = false
			hasProposal = true
		default: // honest / equivocate 正常提議
			proposal = &blockHash
			pvalid = valid
			hasProposal = true
		}
		if hasProposal {
			for _, n := range c.nodes {
				if _, _, step := n.RoundInfo(); step == StepPropose {
					n.ReceiveProposal(proposal, pvalid)
				}
			}
		}

		// 2) 迭代消息直到收斂
		for iter := 0; iter < 6; iter++ {
			var newVotes []*Vote
			for _, n := range c.nodes {
				newVotes = append(newVotes, n.Advance()...)
			}
			if len(newVotes) == 0 {
				break
			}
			c.broadcast(newVotes)
			// 部分誠實節點已達 quorum commit
			pw := 0
			for _, n := range c.nodes {
				if n.behavior == BehaviorOffline {
					continue
				}
				if n.Committed() {
					pw += c.vset.Get(n.address).Power
				}
			}
			if c.vset.HasQuorum(pw) {
				break
			}
		}

		// 3) 判斷最終化
		commitPower := make(map[string]int)
		for _, n := range c.nodes {
			if ch := n.CommittedHash(); ch != nil {
				commitPower[*ch] += c.vset.Get(n.address).Power
			}
		}
		for bh, pw := range commitPower {
			if c.vset.HasQuorum(pw) {
				committed = true
				h := bh
				finalized = &h
			}
		}
		if committed {
			break
		}

		// 4) 超時 view change
		if rnd < c.maxRounds {
			for _, n := range c.nodes {
				n.MoveToNextRound()
			}
		}
	}

	return HeightResult{
		Height:        height,
		Committed:     committed,
		FinalizedHash: finalized,
		Evidence:      c.slashing.Evidence(),
		Slashed:       c.slashedMap(),
		RoundsUsed:    c.roundsUsed(),
	}
}

func (c *BFTCoordinator) slashedMap() map[string]float64 {
	out := make(map[string]float64)
	for _, n := range c.nodes {
		if a := c.slashing.SlashedAmount(n.address); a > 0 {
			out[n.address] = a
		}
	}
	return out
}

func (c *BFTCoordinator) roundsUsed() int32 {
	var maxR int32
	for _, n := range c.nodes {
		if _, r, _ := n.RoundInfo(); r > maxR {
			maxR = r
		}
	}
	return maxR
}

// Checkpoint 為已最終化檢查點。
type Checkpoint struct {
	Height int64  `json:"height"`
	Hash   string `json:"hash"`
	Rounds int32  `json:"rounds"`
	Ts     int64  `json:"ts"`
}

// FinalityLog 記錄已最終化檢查點。並發安全。
type FinalityLog struct {
	mu          sync.Mutex
	checkpoints map[int64]Checkpoint
}

// NewFinalityLog 構造最終化日誌。
func NewFinalityLog() *FinalityLog {
	return &FinalityLog{checkpoints: make(map[int64]Checkpoint)}
}

// Record 記錄一次最終化結果；重複或未 commit 返回 false。
func (l *FinalityLog) Record(r HeightResult) bool {
	if !r.Committed || r.FinalizedHash == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.checkpoints[r.Height]; ok {
		return false
	}
	l.checkpoints[r.Height] = Checkpoint{
		Height: r.Height, Hash: *r.FinalizedHash, Rounds: r.RoundsUsed,
		Ts: time.Now().Unix(),
	}
	return true
}

// IsFinalized 判斷高度是否已最終化。
func (l *FinalityLog) IsFinalized(height int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.checkpoints[height]
	return ok
}

// Latest 返回最新最終化高度。
func (l *FinalityLog) Latest() (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var h int64
	found := false
	for k := range l.checkpoints {
		if !found || k > h {
			h = k
			found = true
		}
	}
	return h, found
}

// Checkpoints 返回檢查點按高度排序的切片。
func (l *FinalityLog) Checkpoints() []Checkpoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Checkpoint, 0, len(l.checkpoints))
	for _, cp := range l.checkpoints {
		out = append(out, cp)
	}
	return out
}

// MakeValidatorSet 生成 n 個等權驗證人（返回集合與密鑰，密鑰僅測試用）。
func MakeValidatorSet(n int, kps []*crypto.KeyPair) (*ValidatorSet, []*crypto.KeyPair, error) {
	if kps == nil {
		kps = make([]*crypto.KeyPair, n)
		for i := 0; i < n; i++ {
			kp, err := crypto.GenerateKeyPair()
			if err != nil {
				return nil, nil, err
			}
			kps[i] = kp
		}
	}
	if len(kps) != n {
		return nil, nil, errValidatorCount
	}
	vals := make([]Validator, n)
	for i, kp := range kps {
		addr, err := kp.Address()
		if err != nil {
			return nil, nil, err
		}
		v, err := NewValidator(addr, hexEncode(kp.PublicKeyCompressed()), 1)
		if err != nil {
			return nil, nil, err
		}
		vals[i] = *v
	}
	vset, err := NewValidatorSet(vals)
	if err != nil {
		return nil, nil, err
	}
	return vset, kps, nil
}
