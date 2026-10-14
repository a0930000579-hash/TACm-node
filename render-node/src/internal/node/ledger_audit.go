package node

// M71 交易與合約層鏈上審計（賬本重放）：
//
// 從創世起逐塊重放全部鏈上交易，獨立驗證：
//  1. 每筆非 coinbase 交易的簽名有效（pubkey 派生地址 == from，簽名匹配）
//  2. nonce 順序正確（同地址嚴格遞增，防重放/亂序）
//  3. 發送方餘額充足（from ≥ amount+fee，防透支/雙花）
//  4. 合約交易（vm:deploy:/vm:call:）memo 格式合法（gas 可解析、payload 可解碼）
//  5. 賬本守恆：全部地址餘額總和 == 全鏈 coinbase 增發總額（無憑空增發）
//
// 任何節點只要同步同一條鏈，重放結果完全一致——鏈上事實，與 M68/M70 同源口徑。

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// ledgerAuditEps 賬本守恆浮點容差（金額精度 1e-6，累加誤差遠小於此）。
const ledgerAuditEps = 1e-6

// isCoinbaseMemo 判定交易是否為 coinbase（memo 恰為 "coinbase" 或以 "coinbase:" 開頭）。
func isCoinbaseMemo(memo string) bool {
	return memo == "coinbase" || strings.HasPrefix(memo, "coinbase:")
}

// LedgerAuditResult 交易與合約層鏈上審計結果（M71 核心結構）。
type LedgerAuditResult struct {
	OK            bool     `json:"ok"`          // 全部檢查通過且賬本守恆
	TipHeight     int64    `json:"tip_height"`  // 重放時的鏈頂
	CoinbaseTx    int      `json:"coinbase_tx"` // 納入守恆的 coinbase 交易數
	TxChecked     int      `json:"tx_checked"`  // 審計的非 coinbase 交易數
	TxSigPass     int      `json:"tx_sig_pass"` // 簽名驗證通過
	TxSigFail     int      `json:"tx_sig_fail"`
	TxNoncePass   int      `json:"tx_nonce_pass"` // nonce 順序通過
	TxNonceFail   int      `json:"tx_nonce_fail"`
	TxBalancePass int      `json:"tx_balance_pass"` // 餘額充足通過
	TxBalanceFail int      `json:"tx_balance_fail"`
	ContractPass  int      `json:"contract_pass"` // 合約交易格式通過
	ContractFail  int      `json:"contract_fail"`
	LedgerOK      bool     `json:"ledger_ok"`           // 守恆：總餘額＋銷毀 == coinbase 增發
	CoinbaseTotal float64  `json:"coinbase_total_tacm"` // 全鏈 coinbase 增發總額
	BalancesTotal float64  `json:"balances_total_tacm"` // 重放後全部地址餘額總和
	BurnedTotal   float64  `json:"burned_total_tacm"`   // 全鏈 EIP-1559 銷毀總額（通縮）
	Issues        []string `json:"issues"`
}

// txToSignMap 把鏈上交易還原為簽名驗證所需的規範 map（與 crypto.TxSighash 字段一致）。
func txToSignMap(t *chaindb.Transaction) map[string]any {
	return map[string]any{
		"from":   t.FromAddr,
		"to":     t.ToAddr,
		"amount": t.Amount,
		"fee":    t.Fee,
		"memo":   t.Memo,
		"ts":     t.Ts,
		"nonce":  t.Nonce,
		"token":  "",
		"pubkey": t.Pubkey,
	}
}

// VerifyLedgerOnChain 全鏈賬本重放審計（M71）。O(鏈長)，任何節點同鏈結果一致。
func (n *Node) VerifyLedgerOnChain() (LedgerAuditResult, error) {
	out := LedgerAuditResult{Issues: []string{}}
	tip := n.db.GetTipHeight()
	out.TipHeight = tip
	if tip < 0 {
		out.LedgerOK = true
		out.OK = true
		return out, nil
	}

	balances := map[string]float64{} // 地址 → 重放餘額（與鏈上 updateBalance 同規則）
	nonces := map[string]int64{}     // 地址 → 已見 nonce（下一筆期望值，從 0 起）
	var coinbaseTotal float64
	var burnedTotal float64 // M74-3：全鏈 EIP-1559 銷毀總額（通縮）

	// 全鏈守恆：sum(balances) 必須 == coinbase 增發總額（誤差容許內）。
	// 重放同時獨立驗證簽名 / nonce / 餘額 / 合約格式——與鏈上 accounts 表無關，
	// 因此能發現「鏈上已接受但實際非法」的交易。
	for h := int64(0); h <= tip; h++ {
		bd, err := n.GetBlockDetail(h)
		if err != nil {
			out.Issues = append(out.Issues,
				"h="+strconv.FormatInt(h, 10)+" 讀取失敗: "+err.Error())
			continue
		}
		if bd == nil || bd.Block == nil {
			out.Issues = append(out.Issues,
				"h="+strconv.FormatInt(h, 10)+" 區塊缺失（鏈不連續）")
			continue
		}
		proposer := bd.Block.ProposerAddress
		if proposer == "" {
			proposer = bd.Block.Proposer
		}
		for i := range bd.Transactions {
			t := bd.Transactions[i]
			if t == nil {
				continue
			}
			amt, aerr := strconv.ParseFloat(strings.TrimSpace(t.Amount), 64)
			if aerr != nil || amt < 0 {
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
					" tx["+strconv.Itoa(t.TxIndex)+"] 金額非法: "+t.Amount)
				continue
			}
			fee, ferr := strconv.ParseFloat(strings.TrimSpace(t.Fee), 64)
			if ferr != nil || fee < 0 {
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
					" tx["+strconv.Itoa(t.TxIndex)+"] 費用非法: "+t.Fee)
				continue
			}

			// 創世塊（h=0）：全部交易視為創世分配，直接 credit（與鏈上 InsertBlock 一致）。
			// coinbase（h>0）：唯一增發源，直接 credit 到收款人。
			if h == 0 || isCoinbaseMemo(t.Memo) {
				out.CoinbaseTx++
				coinbaseTotal += amt
				balances[t.ToAddr] += amt
				continue
			}

			out.TxChecked++
			if t.FromAddr == "" {
				out.TxSigFail++
				out.TxBalanceFail++
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
					" tx["+strconv.Itoa(t.TxIndex)+"] 非 coinbase 但 from 為空")
				continue
			}

			// ① 簽名驗證：pubkey 派生地址 == from 且簽名匹配。
			if t.Pubkey != "" &&
				crypto.VerifyTransactionSignature(txToSignMap(t), t.Signature, t.FromAddr) {
				out.TxSigPass++
			} else {
				out.TxSigFail++
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
					" tx["+strconv.Itoa(t.TxIndex)+"] 簽名驗證失敗")
			}

			// ② nonce 順序：同地址嚴格遞增（下一筆期望 = 已見 + 1）。
			exp := nonces[t.FromAddr]
			if t.Nonce == exp {
				out.TxNoncePass++
				nonces[t.FromAddr] = exp + 1
			} else {
				out.TxNonceFail++
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
					" tx["+strconv.Itoa(t.TxIndex)+"] nonce 異常: 期望 "+strconv.FormatInt(exp, 10)+
					" 實際 "+strconv.FormatInt(t.Nonce, 10))
			}

			// ③ 餘額充足（防透支/雙花）。
			bal := balances[t.FromAddr]
			if bal+ledgerAuditEps >= amt+fee {
				out.TxBalancePass++
			} else {
				out.TxBalanceFail++
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
					" tx["+strconv.Itoa(t.TxIndex)+"] 餘額不足: 餘額 "+strconv.FormatFloat(bal, 'f', 4, 64)+
					" 需 "+strconv.FormatFloat(amt+fee, 'f', 4, 64))
			}

			// ④ 合約交易格式：memo 可解析、gas 合法、payload 可解碼。
			if isContractMemo(t.Memo) {
				gas, payload, perr := splitContractMemo(t.Memo)
				if perr != nil {
					out.ContractFail++
					out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
						" tx["+strconv.Itoa(t.TxIndex)+"] 合約 memo 解析失敗: "+perr.Error())
				} else if _, herr := hexInput(payload); herr != nil {
					out.ContractFail++
					out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
						" tx["+strconv.Itoa(t.TxIndex)+"] 合約 payload hex 非法")
				} else if gas == 0 {
					out.ContractFail++
					out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+
						" tx["+strconv.Itoa(t.TxIndex)+"] 合約 gas 為 0（無資源治理）")
				} else {
					out.ContractPass++
				}
			}

			// M74-3：EIP-1559 銷毀部分從付款人扣除但不入任何帳戶（與 updateBalance 一致）。
			if t.Burned != "" {
				if bb, berr := strconv.ParseFloat(strings.TrimSpace(t.Burned), 64); berr == nil && bb > 0 {
					burnedTotal += bb
				}
			}

			// 應用狀態轉換（與 chaindb.updateBalance 完全一致）。
			balances[t.FromAddr] = bal - amt - fee
			balances[t.ToAddr] += amt
			tip := fee
			if t.Burned != "" {
				if bb, berr := strconv.ParseFloat(strings.TrimSpace(t.Burned), 64); berr == nil {
					tip = fee - bb
				}
			}
			if tip > 0 && proposer != "" && proposer != t.FromAddr {
				balances[proposer] += tip
			}
		}
	}

	// ⑤ 賬本守恆：總餘額 == coinbase 增發總額。
	var balTotal float64
	for _, b := range balances {
		balTotal += b
	}
	out.CoinbaseTotal = roundCoinbase(coinbaseTotal)
	out.BalancesTotal = roundCoinbase(balTotal)
	out.BurnedTotal = roundCoinbase(burnedTotal)
	// M74-3：守恆恆等式 = 總餘額 + 全鏈銷毀 == coinbase 增發（銷毀是通縮，非漏洞）。
	out.LedgerOK = math.Abs(balTotal+burnedTotal-coinbaseTotal) <= ledgerAuditEps
	if !out.LedgerOK {
		out.Issues = append(out.Issues, fmt.Sprintf(
			"賬本守恆失敗: 總餘額 %s + 銷毀 %s != coinbase 增發 %s",
			strconv.FormatFloat(out.BalancesTotal, 'f', 4, 64),
			strconv.FormatFloat(burnedTotal, 'f', 4, 64),
			strconv.FormatFloat(out.CoinbaseTotal, 'f', 4, 64)))
	}

	out.OK = out.TxSigFail == 0 && out.TxNonceFail == 0 &&
		out.TxBalanceFail == 0 && out.ContractFail == 0 && out.LedgerOK
	return out, nil
}
