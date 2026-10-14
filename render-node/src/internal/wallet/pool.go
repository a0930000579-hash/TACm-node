package wallet

// pool.go — 獎勵池／交易所資金池聚合視圖（對齊原本「理財&獎勵池」語義）：
//   池資金 = coinbase 12% 自動挹注 + 交易所手續費 topup；可 withdraw 至地址作為交易所運營資金。
//   提供 balance / total_injected / total_claimed 三視角與最近動帳。

import (
	"database/sql"
	"fmt"
	"math/big"
)

// PoolAssetStat 單一資產的資金池聚合（wei/最小單位）。
type PoolAssetStat struct {
	Asset         string `json:"asset"`
	Balance       string `json:"balance"`
	TotalInjected string `json:"total_injected"`
	TotalClaimed  string `json:"total_claimed"`
	Count         int    `json:"count"`
}

// PoolSummary 交易所資金池聚合（多資產：coinbase 挹注 + topup 注入 − withdraw 領用）。
type PoolSummary struct {
	Assets []PoolAssetStat `json:"assets"`
}

// PoolSummary 聚合 reward_pool 帳戶全部分錄（按資產）。
func (s *Store) PoolSummary() (*PoolSummary, error) {
	rows, err := s.db.Query(`SELECT asset, delta FROM wallet_ledger WHERE account=?`, RewardPoolAddr)
	if err != nil {
		return nil, fmt.Errorf("wallet: 資金池聚合: %w", err)
	}
	defer rows.Close()
	byAsset := map[string]*PoolAssetStat{}
	order := []string{}
	for rows.Next() {
		var asset, delta string
		if err := rows.Scan(&asset, &delta); err != nil {
			return nil, fmt.Errorf("wallet: 讀取資金池分錄: %w", err)
		}
		st, ok := byAsset[asset]
		if !ok {
			st = &PoolAssetStat{Asset: asset}
			byAsset[asset] = st
			order = append(order, asset)
		}
		d, ok := new(big.Int).SetString(delta, 10)
		if !ok {
			continue
		}
		bal := stBig(st.Balance)
		bal.Add(bal, d)
		st.Balance = bal.String()
		if d.Sign() > 0 {
			inj := stBig(st.TotalInjected)
			inj.Add(inj, d)
			st.TotalInjected = inj.String()
		} else {
			cl := stBig(st.TotalClaimed)
			cl.Sub(cl, d) // 負數取絕對值累計
			st.TotalClaimed = cl.String()
		}
		st.Count++
	}
	out := &PoolSummary{Assets: []PoolAssetStat{}}
	for _, a := range order {
		st := *byAsset[a]
		if st.Balance == "" {
			st.Balance = "0"
		}
		if st.TotalInjected == "" {
			st.TotalInjected = "0"
		}
		if st.TotalClaimed == "" {
			st.TotalClaimed = "0"
		}
		out.Assets = append(out.Assets, st)
	}
	return out, rows.Err()
}

// stBig 解析字串為 big.Int；空值視為 0（避免 nil 指標）。
func stBig(v string) *big.Int {
	if v == "" {
		return big.NewInt(0)
	}
	b, ok := new(big.Int).SetString(v, 10)
	if !ok {
		return big.NewInt(0)
	}
	return b
}

var _ = sql.ErrNoRows
