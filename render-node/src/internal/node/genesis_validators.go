package node

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"tacm/internal/consensus/bft"
)

// ConfigureGenesisValidators 從規格字符串構造全局驗證人集並替換本節點的
// BFT 視圖。規格：nodeID:address:pubkeyHex:power，多項以逗號分隔。
// 必須在 Start 前調用。
func (n *Node) ConfigureGenesisValidators(spec string) error {
	vals, err := ParseGenesisValidators(spec)
	if err != nil {
		return err
	}
	vset, err := bft.NewValidatorSet(vals)
	if err != nil {
		return err
	}
	return n.ReplaceValidatorSet(vset)
}

// ParseGenesisValidators 解析創世驗證人規格。
func ParseGenesisValidators(spec string) ([]bft.Validator, error) {
	out := make([]bft.Validator, 0)
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ":")
		if len(fields) != 4 {
			return nil, fmt.Errorf(
				"非法驗證人規格 %q，應為 nodeID:address:pubkey:power", part)
		}
		address := strings.TrimSpace(fields[1])
		pubkey := strings.TrimSpace(fields[2])
		power, err := strconv.Atoi(strings.TrimSpace(fields[3]))
		if err != nil {
			return nil, fmt.Errorf("驗證人權重非法: %w", err)
		}
		v, err := bft.NewValidator(address, pubkey, power)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	if len(out) == 0 {
		return nil, errors.New("創世驗證人為空")
	}
	return out, nil
}
