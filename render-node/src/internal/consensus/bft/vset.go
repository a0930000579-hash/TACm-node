package bft

import (
	"encoding/hex"
	"errors"
	"fmt"

	"tacm/internal/crypto"
)

// NewValidator 構造驗證人，校驗公鑰派生地址一致且權重為正。
func NewValidator(address, pubkeyHex string, power int) (*Validator, error) {
	pub, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("bft: 驗證人公鑰非法: %w", err)
	}
	derived, err := crypto.PubKeyToAddress(pub)
	if err != nil {
		return nil, fmt.Errorf("bft: 派生驗證人地址失敗: %w", err)
	}
	if derived != address {
		return nil, fmt.Errorf("bft: 驗證人地址與公鑰不匹配: %s != %s", address, derived)
	}
	if power <= 0 {
		return nil, errors.New("bft: 投票權重必須 > 0")
	}
	return &Validator{Address: address, PubkeyHex: pubkeyHex, Power: power}, nil
}

// ValidatorSet 為驗證人集與法定人數計算。
type ValidatorSet struct {
	validators []Validator
	byAddr     map[string]int // address → index
	totalPower int
}

// NewValidatorSet 構造非空、地址不重複的驗證人集。
func NewValidatorSet(vals []Validator) (*ValidatorSet, error) {
	if len(vals) == 0 {
		return nil, errors.New("bft: 驗證人集不能為空")
	}
	vs := &ValidatorSet{
		validators: make([]Validator, len(vals)),
		byAddr:     make(map[string]int, len(vals)),
	}
	for i, v := range vals {
		if _, dup := vs.byAddr[v.Address]; dup {
			return nil, fmt.Errorf("bft: 驗證人地址重複: %s", v.Address)
		}
		vs.byAddr[v.Address] = i
		vs.validators[i] = v
		vs.totalPower += v.Power
	}
	return vs, nil
}

// Len 返回驗證人數。
func (vs *ValidatorSet) Len() int { return len(vs.validators) }

// TotalPower 返回總投票權重。
func (vs *ValidatorSet) TotalPower() int { return vs.totalPower }

// Contains 判斷地址是否在集合中。
func (vs *ValidatorSet) Contains(address string) bool {
	_, ok := vs.byAddr[address]
	return ok
}

// Get 返回驗證人；不存在返回 nil。
func (vs *ValidatorSet) Get(address string) *Validator {
	if i, ok := vs.byAddr[address]; ok {
		return &vs.validators[i]
	}
	return nil
}

// At 返回下標處驗證人。
func (vs *ValidatorSet) At(i int) *Validator {
	if i < 0 || i >= len(vs.validators) {
		return nil
	}
	return &vs.validators[i]
}

// FaultTolerance 返回可容忍的最大惡意權重（< total/3）。
func (vs *ValidatorSet) FaultTolerance() int {
	return (vs.totalPower - 1) / 3
}

// QuorumPower 返回達法定人數所需的最小權重（嚴格 > 2/3 total）。
func (vs *ValidatorSet) QuorumPower() int {
	return (2*vs.totalPower)/3 + 1
}

// HasQuorum 判斷 power 是否達法定人數。
func (vs *ValidatorSet) HasQuorum(power int) bool {
	return power >= vs.QuorumPower()
}

// Proposer 按高度+輪確定性輪換提議者。
func (vs *ValidatorSet) Proposer(height int64, round int32) *Validator {
	idx := int((height + int64(round)) % int64(len(vs.validators)))
	return &vs.validators[idx]
}

// List 返回驗證人切片副本。
func (vs *ValidatorSet) List() []Validator {
	out := make([]Validator, len(vs.validators))
	copy(out, vs.validators)
	return out
}
