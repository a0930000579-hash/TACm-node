package wallet

import (
	"fmt"
	"math/big"
)

// LedgerKind 為帳本分錄類型（審計標籤）。
type LedgerKind string

const (
	KindTransferOut LedgerKind = "transfer_out"
	KindTransferIn  LedgerKind = "transfer_in"
	KindMint        LedgerKind = "mint"
	KindBurn        LedgerKind = "burn"
	KindFee         LedgerKind = "fee"
	KindPayout      LedgerKind = "payout"
	KindDeposit     LedgerKind = "deposit"
	KindWithdraw    LedgerKind = "withdraw"
	KindReward      LedgerKind = "reward"
)

// LedgerEntry 為一條審計分錄：某帳戶某資產的餘額變動。
// 同一業務操作產生的分錄列表之和恆等於 0（雙分錄不變式）。
type LedgerEntry struct {
	ID      int64      `json:"id"`
	Ts      int64      `json:"ts"`
	Kind    LedgerKind `json:"kind"`
	Asset   Asset      `json:"asset"`
	Account string     `json:"account"`
	Delta   string     `json:"delta"` // 最小單位十進制（正/負）
	Memo    string     `json:"memo,omitempty"`
}

// Entry 構建一條分錄（Delta 為十進制最小單位字符串，或 big.Int/Amount）。
func Entry(kind LedgerKind, account string, asset Asset, delta any, memo string) LedgerEntry {
	e := LedgerEntry{Kind: kind, Asset: asset, Account: account, Memo: memo}
	switch v := delta.(type) {
	case *big.Int:
		e.Delta = v.String()
	case Amount:
		if v.Big != nil {
			e.Delta = v.Big.String()
		} else {
			e.Delta = fmt.Sprintf("%d", v.I64)
		}
	case int64:
		e.Delta = fmt.Sprintf("%d", v)
	case int:
		e.Delta = fmt.Sprintf("%d", v)
	case string:
		e.Delta = v
	default:
		e.Delta = "0"
	}
	return e
}

// checkInvariant 校驗一組分錄的資金守恆：站內流動（轉帳/手續費/提現等）分錄
// 之和必須為 0；外部增發（mint/reward/deposit，打破守恆）允許存在但不參與判定。
func checkInvariant(entries []LedgerEntry) error {
	var flow big.Int
	hasFlow := false
	for _, e := range entries {
		v, ok := new(big.Int).SetString(e.Delta, 10)
		if !ok {
			return fmt.Errorf("帳本分錄金額非法 %q", e.Delta)
		}
		switch e.Kind {
		case KindMint, KindDeposit, KindReward:
			// 外部增發來源，不參與守恆檢查。
		default:
			hasFlow = true
			flow.Add(&flow, v)
		}
	}
	if hasFlow && flow.Sign() != 0 {
		return fmt.Errorf("帳本不變式被破壞: 站內流動和=%s", flow.String())
	}
	return nil
}
