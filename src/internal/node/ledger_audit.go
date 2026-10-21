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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
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
	// v60：增量審計。Incremental=true 表示本次僅重放自 FromHeight+1 起的區塊
	// （基線取自已持久化的 audit_checkpoint.json），與全量重放結果一致。
	Incremental bool  `json:"incremental"`
	FromHeight  int64 `json:"from_height"`

	// 非序列化：重放狀態快照（供 checkpointFromResult 持久化基線）。
	balances map[string]float64
	nonces   map[string]int64
	// 非序列化：原始（未 round）累加值。基線必須存原始精度——roundCoinbase
	// 收斂到 1e-4，若基線存 round 值，增量/直取路徑守恆檢查混入 1e-4 級誤差
	// 會誤報 ledger_ok=false（v60.1 修正：主鏈 7.5 萬塊實測觸發）。
	coinbaseTotalRaw float64
	burnedTotalRaw   float64
}

// AuditCheckpoint v60：審計增量基線（僅在重放無任何異常時持久化）。
// 鏈為 append-only，基線高度後的區塊可安全增量重放；偵測到鏈回滾
// （tip < Height）或檔案損壞時自動退回全量重放。
type AuditCheckpoint struct {
	// V 基線格式版本。=1 表示 CoinbaseTotal/BurnedTotal 為原始（未 round）精度；
	// 舊版（v60.0，無 V，存 1e-4 round 值）載入時被視為無效 → 強制全量重建，
	// 避免守恆檢查混入 round 誤差。
	V int `json:"v"`
	Height        int64              `json:"height"`
	CoinbaseTx    int                `json:"coinbase_tx"`
	TxChecked     int                `json:"tx_checked"`
	TxSigPass     int                `json:"tx_sig_pass"`
	TxSigFail     int                `json:"tx_sig_fail"`
	TxNoncePass   int                `json:"tx_nonce_pass"`
	TxNonceFail   int                `json:"tx_nonce_fail"`
	TxBalancePass int                `json:"tx_balance_pass"`
	TxBalanceFail int                `json:"tx_balance_fail"`
	ContractPass  int                `json:"contract_pass"`
	ContractFail  int                `json:"contract_fail"`
	CoinbaseTotal float64            `json:"coinbase_total"`
	BurnedTotal   float64            `json:"burned_total"`
	Balances      map[string]float64 `json:"balances"`
	Nonces        map[string]int64   `json:"nonces"`
}

// auditCheckpointPath 審計基線檔路徑（節點資料目錄）。
func (n *Node) auditCheckpointPath() string {
	return filepath.Join(n.DataDir(), "audit_checkpoint.json")
}

// loadAuditCheckpoint 讀取審計基線；檔案不存在或損壞回傳 nil（→全量重放）。
func loadAuditCheckpoint(path string) *AuditCheckpoint {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cp AuditCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil
	}
	if cp.V != 1 || cp.Height < 0 || cp.Balances == nil || cp.Nonces == nil {
		return nil
	}
	return &cp
}

// saveAuditCheckpoint 持久化審計基線（僅在重放無異常時呼叫）。
func saveAuditCheckpoint(path string, cp *AuditCheckpoint) error {
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
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

// contractMemoHasGas 判斷合約 memo 是否為「帶 gas 段」的新格式（<gas>:<hex>）。
// 舊格式（vm:deploy:<hex> / vm:call:<hex>）無 gas 段，gas 天然為 0，
// 屬早期合法格式（v55：審計不再將其標為「合約 gas 為 0」）。
func contractMemoHasGas(memo string) bool {
	rest := memo
	switch {
	case strings.HasPrefix(memo, vmDeployPrefix):
		rest = memo[len(vmDeployPrefix):]
	case strings.HasPrefix(memo, vmCallPrefix):
		rest = memo[len(vmCallPrefix):]
	default:
		return false
	}
	return strings.Contains(rest, ":")
}

// VerifyLedgerOnChain 鏈上審計（M71）。v60 起支援增量重放：
// 已持久化無異常基線（audit_checkpoint.json）時只重放自基線高度起的區塊，
// 結果與全量重放一致（append-only 鏈，基線後區塊不影響基線狀態）。
// 鏈回滾或基線缺失/損壞時自動退回全量重放。
func (n *Node) VerifyLedgerOnChain() (LedgerAuditResult, error) {
	tip := n.db.GetTipHeight()
	if tip < 0 {
		out := LedgerAuditResult{Issues: []string{}, TipHeight: tip, LedgerOK: true, OK: true}
		return out, nil
	}
	cp := loadAuditCheckpoint(n.auditCheckpointPath())
	if cp == nil || cp.Height > tip {
		// 無基線（首次/損壞）或鏈回滾：全量重放，成功後建立基線。
		res := n.auditReplay(0, nil, tip)
		if res.TxSigFail == 0 && res.TxNonceFail == 0 && res.TxBalanceFail == 0 &&
			res.ContractFail == 0 && res.LedgerOK {
			_ = saveAuditCheckpoint(n.auditCheckpointPath(), checkpointFromResult(res))
		}
		return res, nil
	}
	if cp.Height == tip {
		// 基線已達鏈頂：直接由基線組結果（零重放）。
		res := auditResultFromCheckpoint(cp, tip)
		res.Incremental = true
		res.FromHeight = cp.Height
		return res, nil
	}
	// 增量重放自基線高度+1 起的區塊。
	res := n.auditReplay(cp.Height+1, cp, tip)
	res.Incremental = true
	res.FromHeight = cp.Height
	if res.TxSigFail == 0 && res.TxNonceFail == 0 && res.TxBalanceFail == 0 &&
		res.ContractFail == 0 && res.LedgerOK {
		_ = saveAuditCheckpoint(n.auditCheckpointPath(), checkpointFromResult(res))
	}
	return res, nil
}

// checkpointFromResult 由無異常審計結果生成基線（CoinbaseTotal/BurnedTotal 存原始精度，
// 守恆檢查與重放全程一致；round 僅用於顯示）。
func checkpointFromResult(r LedgerAuditResult) *AuditCheckpoint {
	return &AuditCheckpoint{
		V:             1,
		Height:        r.TipHeight,
		CoinbaseTx:    r.CoinbaseTx,
		TxChecked:     r.TxChecked,
		TxSigPass:     r.TxSigPass,
		TxSigFail:     r.TxSigFail,
		TxNoncePass:   r.TxNoncePass,
		TxNonceFail:   r.TxNonceFail,
		TxBalancePass: r.TxBalancePass,
		TxBalanceFail: r.TxBalanceFail,
		ContractPass:  r.ContractPass,
		ContractFail:  r.ContractFail,
		CoinbaseTotal: r.coinbaseTotalRaw,
		BurnedTotal:   r.burnedTotalRaw,
		Balances:      r.balances,
		Nonces:        r.nonces,
	}
}

// auditResultFromCheckpoint 由基線直接組審計結果（基線高度==鏈頂時）。
func auditResultFromCheckpoint(cp *AuditCheckpoint, tip int64) LedgerAuditResult {
	out := LedgerAuditResult{Issues: []string{}, TipHeight: tip}
	out.CoinbaseTx = cp.CoinbaseTx
	out.TxChecked = cp.TxChecked
	out.TxSigPass = cp.TxSigPass
	out.TxSigFail = cp.TxSigFail
	out.TxNoncePass = cp.TxNoncePass
	out.TxNonceFail = cp.TxNonceFail
	out.TxBalancePass = cp.TxBalancePass
	out.TxBalanceFail = cp.TxBalanceFail
	out.ContractPass = cp.ContractPass
	out.ContractFail = cp.ContractFail
	out.CoinbaseTotal = roundCoinbase(cp.CoinbaseTotal)
	out.BurnedTotal = roundCoinbase(cp.BurnedTotal)
	var balTotal float64
	for _, b := range cp.Balances {
		balTotal += b
	}
	out.BalancesTotal = roundCoinbase(balTotal)
	out.LedgerOK = math.Abs(balTotal+cp.BurnedTotal-cp.CoinbaseTotal) <= ledgerAuditEps
	out.OK = out.TxSigFail == 0 && out.TxNonceFail == 0 &&
		out.TxBalanceFail == 0 && out.ContractFail == 0 && out.LedgerOK
	return out
}

// auditReplay 重放 [start, tip] 區塊（start=0 且 cp=nil 即全量）。基線狀態由 cp 提供；
// 收尾做賬本守恆並把重放狀態回填到結果（供 checkpointFromResult 使用）。
func (n *Node) auditReplay(start int64, cp *AuditCheckpoint, tip int64) LedgerAuditResult {
	out := LedgerAuditResult{Issues: []string{}, TipHeight: tip}
	balances := map[string]float64{}
	nonces := map[string]int64{}
	if cp != nil {
		for k, v := range cp.Balances {
			balances[k] = v
		}
		for k, v := range cp.Nonces {
			nonces[k] = v
		}
		out.CoinbaseTx = cp.CoinbaseTx
		out.TxChecked = cp.TxChecked
		out.TxSigPass = cp.TxSigPass
		out.TxSigFail = cp.TxSigFail
		out.TxNoncePass = cp.TxNoncePass
		out.TxNonceFail = cp.TxNonceFail
		out.TxBalancePass = cp.TxBalancePass
		out.TxBalanceFail = cp.TxBalanceFail
		out.ContractPass = cp.ContractPass
		out.ContractFail = cp.ContractFail
	}
	var coinbaseTotal = cpCoinbaseTotal(cp)
	var burnedTotal = cpBurnedTotal(cp)

	for h := start; h <= tip; h++ {
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
			// 治理系統交易（memo=gov:treasury:N，由出塊引擎依治理決議自動建單、
			// 無簽名）語意同 coinbase，全節點照單執行——不適用用戶簽名驗證
			// （v55：修正審計誤報，治理撥款不再被標為「簽名驗證失敗」）。
			if strings.HasPrefix(t.Memo, "gov:") {
				out.TxSigPass++
			} else if t.Pubkey != "" &&
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
				} else if gas == 0 && contractMemoHasGas(t.Memo) {
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

	// ⑤ 賬本守恆：總餘額 + 銷毀 == coinbase 增發總額。
	var balTotal float64
	for _, b := range balances {
		balTotal += b
	}
	out.CoinbaseTotal = roundCoinbase(coinbaseTotal)
	out.BalancesTotal = roundCoinbase(balTotal)
	out.BurnedTotal = roundCoinbase(burnedTotal)
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
	// 回填重放狀態（含原始累加值），供 checkpointFromResult 持久化基線。
	out.balances = balances
	out.nonces = nonces
	out.coinbaseTotalRaw = coinbaseTotal
	out.burnedTotalRaw = burnedTotal
	return out
}

// cpCoinbaseTotal / cpBurnedTotal 取基線累計值（無基線時為 0）。
func cpCoinbaseTotal(cp *AuditCheckpoint) float64 {
	if cp == nil {
		return 0
	}
	return cp.CoinbaseTotal
}

func cpBurnedTotal(cp *AuditCheckpoint) float64 {
	if cp == nil {
		return 0
	}
	return cp.BurnedTotal
}
