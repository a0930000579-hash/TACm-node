package bft

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"tacm/internal/crypto"
)

// Behavior 為驗證人行為模式。
const (
	BehaviorHonest     = "honest"
	BehaviorOffline    = "offline"
	BehaviorEquivocate = "equivocate"
	BehaviorWrongBlock = "wrongblock"
)

// BFTNode 為單個驗證人的 BFT 狀態機。並發安全。
type BFTNode struct {
	kp       *crypto.KeyPair
	address  string
	vset     *ValidatorSet
	behavior string

	mu    sync.Mutex
	state *RoundState
}

// NewBFTNode 構造驗證人節點。
func NewBFTNode(kp *crypto.KeyPair, vset *ValidatorSet, behavior string) (*BFTNode, error) {
	if kp == nil || vset == nil {
		return nil, errors.New("bft: 密鑰或驗證人集為空")
	}
	switch behavior {
	case BehaviorHonest, BehaviorOffline, BehaviorEquivocate, BehaviorWrongBlock:
	default:
		return nil, fmt.Errorf("bft: 未知行為: %s", behavior)
	}
	addr, err := kp.Address()
	if err != nil {
		return nil, err
	}
	return &BFTNode{kp: kp, address: addr, vset: vset, behavior: behavior}, nil
}

// Address 返回驗證人地址。
func (n *BFTNode) Address() string { return n.address }

func newRoundState(height int64, round int32) *RoundState {
	return &RoundState{
		Height: height, Round: round, Step: StepPropose,
		prevotes:   make(map[string]map[string]int),
		precommits: make(map[string]map[string]int),
	}
}

// StartHeight 初始化某高度的共識（round 0）。
func (n *BFTNode) StartHeight(height int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state = newRoundState(height, 0)
}

// ReceiveProposal 接收提議；僅在 propose 步有效。
func (n *BFTNode) ReceiveProposal(blockHash *string, valid bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st == nil || st.Step != StepPropose {
		return
	}
	st.proposalHash = blockHash
	st.proposalValid = valid
	st.Step = StepPrevote
}

func (n *BFTNode) addToBucket(bucket map[string]map[string]int, v *Vote) {
	power := 0
	if val := n.vset.Get(v.Validator); val != nil {
		power = val.Power
	}
	bk := bucketKey(v.BlockHash)
	m, ok := bucket[bk]
	if !ok {
		m = make(map[string]int)
		bucket[bk] = m
	}
	m[v.Validator] = power
}

// polka 返回獲 >2/3 prevote 的塊；(nil,true) 表示 nil 塊獲 polka；(_,false) 無 polka。
func (n *BFTNode) polka(st *RoundState) (*string, bool) {
	for bk, m := range st.prevotes {
		sum := 0
		for _, p := range m {
			sum += p
		}
		if n.vset.HasQuorum(sum) {
			if bk == "" {
				return nil, true
			}
			s := bk
			return &s, true
		}
	}
	return nil, false
}

// ReceiveVote 驗證並接收一張投票；高度/輪不匹配或簽名無效均返回錯誤。
func (n *BFTNode) ReceiveVote(v *Vote) error {
	if err := VerifyVote(v, n.vset); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st == nil || v.Height != st.Height || v.Round != st.Round {
		return errors.New("投票高度/輪不匹配")
	}
	if v.Type == Prevote {
		n.addToBucket(st.prevotes, v)
	} else {
		n.addToBucket(st.precommits, v)
	}
	return nil
}

func (n *BFTNode) signAndCollect(st *RoundState, vtype string, bh *string) (*Vote, error) {
	v, err := SignVote(n.kp, vtype, st.Height, st.Round, bh)
	if err != nil {
		return nil, err
	}
	if vtype == Prevote {
		n.addToBucket(st.prevotes, v)
	} else {
		n.addToBucket(st.precommits, v)
	}
	return v, nil
}

// Advance 推進狀態，返回本輪需廣播的投票；已 commit、無狀態或離線返回空。
func (n *BFTNode) Advance() []*Vote {
	n.mu.Lock()
	defer n.mu.Unlock()

	st := n.state
	if st == nil || st.Step == StepCommitted || n.behavior == BehaviorOffline {
		return nil
	}
	out := make([]*Vote, 0, 2)

	// --- PreVote（冪等）---
	if st.Step == StepPrevote && !st.sentPrevote {
		st.sentPrevote = true
		switch n.behavior {
		case BehaviorEquivocate:
			v1, _ := n.signAndCollect(st, Prevote, st.proposalHash)
			v2, _ := n.signAndCollect(st, Prevote, ptrString(strings.Repeat("deadbeef", 8)))
			out = append(out, v1, v2)
		case BehaviorWrongBlock:
			v, _ := n.signAndCollect(st, Prevote, ptrString(strings.Repeat("0badc0de", 8)))
			out = append(out, v)
		default: // honest
			target := st.lockedHash
			if target == nil {
				if st.proposalValid {
					target = st.proposalHash
				}
			}
			v, _ := n.signAndCollect(st, Prevote, target)
			out = append(out, v)
		}
		st.Step = StepPrecommit
		return out
	}

	// --- PreCommit（冪等）---
	if st.Step == StepPrecommit && !st.sentPrecommit {
		polkaHash, polkaOK := n.polka(st)
		switch {
		case n.behavior == BehaviorWrongBlock:
			st.sentPrecommit = true
			v, _ := n.signAndCollect(st, Precommit, ptrString(strings.Repeat("0badc0de", 8)))
			out = append(out, v)
		case polkaOK:
			st.lockedHash = polkaHash
			st.sentPrecommit = true
			if n.behavior == BehaviorEquivocate {
				v1, _ := n.signAndCollect(st, Precommit, polkaHash)
				v2, _ := n.signAndCollect(st, Precommit, ptrString(strings.Repeat("f00dface", 8)))
				out = append(out, v1, v2)
			} else {
				v, _ := n.signAndCollect(st, Precommit, polkaHash)
				out = append(out, v)
			}
		case n.behavior == BehaviorHonest:
			st.sentPrecommit = true
			v, _ := n.signAndCollect(st, Precommit, nil) // nil precommit
			out = append(out, v)
		}
	}

	// --- >2/3 precommit 同塊 → commit 最終化 ---
	if st.Step == StepPrecommit {
		for bk, m := range st.precommits {
			if bk == "" {
				continue
			}
			sum := 0
			for _, p := range m {
				sum += p
			}
			if n.vset.HasQuorum(sum) {
				s := bk
				st.committedHash = &s
				st.Step = StepCommitted
				break
			}
		}
	}
	return out
}

// MoveToNextRound 超時 view change：round+1，保留鎖定，回到 propose。
func (n *BFTNode) MoveToNextRound() {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st == nil || st.Step == StepCommitted {
		return
	}
	n.resetRoundState(st, st.Round+1)
}

// MoveToRound 直接推進到指定 round（僅允許大於當前），保留鎖定並重置
// 投票/提議。用於響應網絡 view-change 消息，避免逐輪等待。
func (n *BFTNode) MoveToRound(target int32) {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.state
	if st == nil || st.Step == StepCommitted || target <= st.Round {
		return
	}
	n.resetRoundState(st, target)
}

func (n *BFTNode) resetRoundState(st *RoundState, round int32) {
	st.Round = round
	st.prevotes = make(map[string]map[string]int)
	st.precommits = make(map[string]map[string]int)
	st.proposalHash = nil
	st.proposalValid = false
	st.sentPrevote = false
	st.sentPrecommit = false
	st.Step = StepPropose
}

// Committed 判斷是否已最終化。
func (n *BFTNode) Committed() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state != nil && n.state.Step == StepCommitted
}

// CommittedHash 返回已最終化的塊哈希；未 commit 返回 nil。
func (n *BFTNode) CommittedHash() *string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state != nil && n.state.Step == StepCommitted {
		return n.state.committedHash
	}
	return nil
}

// RoundInfo 返回當前高度/輪/步（未開始 height=-1）。
func (n *BFTNode) RoundInfo() (height int64, round int32, step string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state == nil {
		return -1, 0, ""
	}
	return n.state.Height, n.state.Round, n.state.Step
}
