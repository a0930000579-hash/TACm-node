package bft

import (
	"sync"
)

type voteKey struct {
	validator string
	height    int64
	round     int32
	vtype     string
}

// SlashingTracker 檢測雙重投票（equivocation）並生成罰沒證據。並發安全。
type SlashingTracker struct {
	mu          sync.Mutex
	seen        map[voteKey]map[string]*Vote
	evidence    []SlashingEvidence
	slashed     map[string]float64
	slashAmount float64
}

// NewSlashingTracker 構造追蹤器，slashAmount 為每次雙簽罰沒金額。
func NewSlashingTracker(slashAmount float64) *SlashingTracker {
	return &SlashingTracker{
		seen:        make(map[voteKey]map[string]*Vote),
		slashed:     make(map[string]float64),
		slashAmount: slashAmount,
	}
}

// Observe 記錄一張投票；若檢測到同高度/輪對不同塊雙簽，返回罰沒證據。
// 同票重放不重複計，返回 nil。
func (t *SlashingTracker) Observe(v *Vote) *SlashingEvidence {
	key := voteKey{v.Validator, v.Height, v.Round, v.Type}
	bk := bucketKey(v.BlockHash)

	t.mu.Lock()
	defer t.mu.Unlock()

	bucket, ok := t.seen[key]
	if !ok {
		bucket = make(map[string]*Vote)
		t.seen[key] = bucket
	}
	if _, dup := bucket[bk]; dup {
		return nil // 同票重放
	}
	var ev *SlashingEvidence
	if len(bucket) > 0 {
		var other *Vote
		for _, ov := range bucket { // 取任一已見衝突票
			other = ov
			break
		}
		e := SlashingEvidence{
			Validator: v.Validator, Height: v.Height, Round: v.Round,
			VoteType: v.Type, HashA: other.BlockHash, HashB: v.BlockHash,
			SigA: other.Signature, SigB: v.Signature,
		}
		t.evidence = append(t.evidence, e)
		t.slashed[v.Validator] += t.slashAmount
		ev = &e
	}
	bucket[bk] = v
	return ev
}

// Evidence 返回累計罰沒證據副本。
func (t *SlashingTracker) Evidence() []SlashingEvidence {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]SlashingEvidence, len(t.evidence))
	copy(out, t.evidence)
	return out
}

// SlashedAmount 返回地址累計罰沒金額。
func (t *SlashingTracker) SlashedAmount(address string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.slashed[address]
}
