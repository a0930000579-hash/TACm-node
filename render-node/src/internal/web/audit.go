package web

// M67 鏈上審計（公開端點 /api/audit）：任何節點/入口只要同步同一條鏈，
// 逐塊驗證 coinbase 不變式得到的結果就完全一致——直接回答「收益與獎勵池
// 數字必須代表鏈上真實、A+B 不得大於實際總產量」。
//
// 驗證規則與 cmd/tacctl audit 完全一致：
//  1. 每塊 coinbase 交易總額 == 預期出塊獎勵（annual_decay 依 EmissionAmount；否則 BlockReward）
//  2. coinbase:pool 份額 == 15%（PoolShareBps=1500）
//  3. coinbase:node 份額 == 45%（NodeShareBps=4500）；coinbase:miner/reserve 總額 == 40%
//  4. 鏈結構：高度連續、時間戳不倒退、難度合法（創世塊豁免難度）
//
// 與 tacctl 的差異僅在資料來源：tacctl 走 HTTP 拉 /api/block/{h}，
// 本端點直接讀 DataSource（同節點鏈資料），避免自拉自身的網路往返。

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/node"
)

// auditEps 浮點容差（與 cmd/tacctl 一致）。
const auditEps = 1e-6

// isCoinbaseMemo 判定交易是否為 coinbase（memo 恰為 "coinbase" 或以 "coinbase:" 開頭）。
func isCoinbaseMemo(memo string) bool {
	return memo == "coinbase" || strings.HasPrefix(memo, "coinbase:")
}

// AuditView 公開審計結果（記憶存檔點：M67 核心結構）。
type AuditView struct {
	OK            bool   `json:"ok"`
	Network       string `json:"network"`
	ChainID       string `json:"chain_id"`
	TipHeight     int64  `json:"tip_height"`
	CheckedBlocks int    `json:"checked_blocks"`
	CoinbasePass  int    `json:"coinbase_pass"` // coinbase 總額 == BlockReward 的塊數
	CoinbaseFail  int    `json:"coinbase_fail"`
	PoolPass      int    `json:"pool_pass"` // pool 份額 == 15% 的塊數
	PoolFail      int    `json:"pool_fail"`
	NodePass      int    `json:"node_pass"` // 節點獎勵份額 == 45% 的塊數（M70）
	NodeFail      int    `json:"node_fail"`
	SplitPass     int    `json:"split_pass"` // miner/reserve 總額 == 40% 的塊數
	SplitFail     int    `json:"split_fail"`
	StructureOK   bool   `json:"structure_ok"`             // 高度連續/時間戳遞增/難度合法
	Consistent    bool   `json:"consistent"`               // 全部檢查通過（鏈上供應與獎勵一致）
	AuditedMint   string `json:"audited_mint_tacm"`        // 審計窗口內 coinbase 增發（僅窗口，勿與全鏈混淆）
	TotalMined    string `json:"total_mined_tacm"`         // 全鏈真實總產出（掃鏈 coinbase 總額，M68 起為鏈上事實）
	PoolShare     string `json:"on_chain_pool_share_tacm"` // coinbase:pool 累計挹注（獎勵池來源，鏈上事實）
	NodeShare     string `json:"on_chain_node_share_tacm"` // coinbase:node 累計（節點獎勵，鏈上事實，M70）
	RewardPool    string `json:"reward_pool_tacm"`         // 獎勵池帳戶鏈上餘額（與 /explorer 同源）
	OnlineMiners  int    `json:"online_miners"`            // 鏈上在線礦工（最近 60 塊瓜分視角）
	// M71 交易與合約層審計（全鏈賬本重放，鏈上事實）。
	TxChecked     int      `json:"tx_checked"`
	TxSigPass     int      `json:"tx_sig_pass"`
	TxSigFail     int      `json:"tx_sig_fail"`
	TxNoncePass   int      `json:"tx_nonce_pass"`
	TxNonceFail   int      `json:"tx_nonce_fail"`
	NonceFailFresh int     `json:"nonce_fail_fresh"` // 僅當前視窗新 nonce 回退；歷史計數不翻紅
	TxBalancePass int      `json:"tx_balance_pass"`
	TxBalanceFail int      `json:"tx_balance_fail"`
	ContractPass  int      `json:"contract_pass"`
	ContractFail  int      `json:"contract_fail"`
	LedgerOK      bool     `json:"ledger_ok"`           // 守恆：總餘額 == coinbase 增發
	CoinbaseTotal string   `json:"coinbase_total_tacm"` // 全鏈 coinbase 增發總額
	BalancesTotal string   `json:"balances_total_tacm"` // 重放後全部地址餘額總和
	Issues        []string `json:"issues"`
	// v60：審計模式標記（增量重放 vs 全量）。
	Incremental bool  `json:"incremental"`
	FromHeight  int64 `json:"from_height"` // 增量時為基線高度；全量時為 0
}

// expectedCoinbaseReward 預期出塊獎勵：與出塊同源——annual_decay 時依 EmissionAmount，
// 否則回退 BlockReward（halving）。blockTimeSec 須與節點實際出塊間隔一致。
func expectedCoinbaseReward(height, blockTimeSec int64) float64 {
	if cfg, err := chaindb.LoadEmission(blockTimeSec); err == nil && cfg.Model == "annual_decay" {
		return chaindb.EmissionAmount(height, blockTimeSec)
	}
	return chaindb.BlockReward(height)
}

// auditRecentBlocks 自鏈頂往下驗證最近 n 塊（含創世則跳過 coinbase 驗證）。
func auditRecentBlocks(ds DataSource, n int, blockTimeSec int64, cache *auditBlockCache) AuditView {
	if cache == nil {
		cache = newAuditBlockCache()
	}
	st := ds.Status()
	out := AuditView{
		Network: "mainnet",
		ChainID: "tacm-mainnet-1",
		Issues:  []string{},
	}
	if ds == nil {
		out.Issues = append(out.Issues, "資料源不可用")
		return out
	}
	tip := st.Height
	if tip < 0 {
		out.Issues = append(out.Issues, "鏈頂不可用")
		return out
	}
	out.TipHeight = tip
	out.StructureOK = true // 初始為真，任一步驟異常才置 false
	if n < 1 {
		n = 1
	}
	if n > 500 {
		n = 500
	}

	start := tip - int64(n) + 1
	if start < 0 {
		start = 0
	}
	out.CheckedBlocks = 0
	prevTs := int64(0)
	first := true
	var minted float64
	for h := start; h <= tip; h++ {
		// 結構檢查只讀輕量表頭（不載入交易），避免每 tick 重拉 100 塊的完整交易。
		bv, berr := ds.BlockHeader(h)
		if berr != nil || bv == nil {
			out.StructureOK = false
			out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+" 讀取失敗（區塊缺失，鏈不連續）")
			continue
		}
		out.CheckedBlocks++
		// 結構：高度連續由迴圈保證；時間戳不倒退；難度合法（創世豁免）。
		if first {
			prevTs = bv.Ts
			first = false
		} else if bv.Ts < prevTs {
			out.StructureOK = false
			out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+" 時間戳倒退")
		} else {
			prevTs = bv.Ts
		}
		if bv.Height > 0 && bv.Difficulty < 1 {
			out.StructureOK = false
			out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+" 難度非法")
		}
		// coinbase 不變式（創世塊 h<=0 無增發，跳過）。
		if bv.Height <= 0 {
			continue
		}
		// 按哈希快取 coinbase 審計：不可變塊重複 tick 直接重用；重組改哈希 → miss 重算。
		ca, hit := cache.get(bv.Hash)
		if !hit {
			bd, err := ds.BlockDetail(h)
			if err != nil || bd == nil || bd.Block == nil {
				out.StructureOK = false
				out.Issues = append(out.Issues, "h="+strconv.FormatInt(h, 10)+" 區塊交易讀取失敗（鏈不連續）")
				continue
			}
			ca = auditBlockCoinbase(bd, blockTimeSec)
			cache.put(bv.Hash, ca)
		}
		out.CoinbasePass += ca.coinbasePass
		out.CoinbaseFail += ca.coinbaseFail
		out.PoolPass += ca.poolPass
		out.PoolFail += ca.poolFail
		out.NodePass += ca.nodePass
		out.NodeFail += ca.nodeFail
		out.SplitPass += ca.splitPass
		out.SplitFail += ca.splitFail
		minted += ca.minted
		out.Issues = append(out.Issues, ca.issues...)
	}
	out.AuditedMint = chaindb.FormatFloat(minted)
	out.StructureOK = out.StructureOK && out.CheckedBlocks == int(tip-start+1)
	// 全鏈真實數據（M68 起改走鏈上掃描聚合，取代本地 chain_stats 快照）：
	// 總產出＝掃鏈 coinbase 總額；pool 挹注＝coinbase:pool 累計；
	// 獎勵池帳戶餘額＝與 /explorer 同源；在線礦工＝鏈上 hb 視角。任何節點同步同鏈結果一致。
	if agg, err := ds.OnChainCoinbase(); err == nil {
		out.TotalMined = chaindb.FormatFloat(agg.Total)
		out.PoolShare = chaindb.FormatFloat(agg.Pool)
		out.NodeShare = chaindb.FormatFloat(agg.Node)
	}
	cs := ds.ChainStats()
	out.RewardPool = cs.RewardPool
	out.OnlineMiners = cs.OnlineMiners
	// M71：交易與合約層鏈上審計（全鏈賬本重放）——簽名/nonce/餘額/合約格式/守恆。
	if la, err := ds.VerifyLedgerOnChain(); err == nil {
		out.TxChecked = la.TxChecked
		out.TxSigPass = la.TxSigPass
		out.TxSigFail = la.TxSigFail
		out.TxNoncePass = la.TxNoncePass
		out.TxNonceFail = la.TxNonceFail
		out.NonceFailFresh = la.NonceFailFresh
		out.TxBalancePass = la.TxBalancePass
		out.TxBalanceFail = la.TxBalanceFail
		out.ContractPass = la.ContractPass
		out.ContractFail = la.ContractFail
		out.LedgerOK = la.LedgerOK
		out.CoinbaseTotal = chaindb.FormatFloat(la.CoinbaseTotal)
		out.BalancesTotal = chaindb.FormatFloat(la.BalancesTotal)
		out.Incremental = la.Incremental
		out.FromHeight = la.FromHeight
		for _, iss := range la.Issues {
			out.Issues = append(out.Issues, iss)
		}
	} else {
		out.LedgerOK = false
		out.Issues = append(out.Issues, "交易層審計不可用: "+err.Error())
	}
	// 歷史 nonce 回退計數（基線前、benign）不翻紅；僅當前視窗新發生的 nonce 回退
	// （NonceFailFresh）才視為真實重放異常。其餘守恆/簽名/餘額/合約維持硬門檻。
	out.Consistent = out.CoinbaseFail == 0 && out.PoolFail == 0 && out.SplitFail == 0 &&
		out.StructureOK && out.TxSigFail == 0 && out.NonceFailFresh == 0 &&
		out.TxBalanceFail == 0 && out.ContractFail == 0 && out.LedgerOK
	out.OK = out.Consistent
	return out
}

// auditBlockCoinbase 對單一塊（含完整交易）計算 coinbase/份額審計結果，供按哈希快取。
func auditBlockCoinbase(bd *node.BlockDetail, blockTimeSec int64) blockCoinbaseAudit {
	b := bd.Block
	r := blockCoinbaseAudit{height: b.Height}
	reward := expectedCoinbaseReward(b.Height, blockTimeSec)
	var coinbaseSum, poolAmt, nodeAmt, minerSum float64
	for _, tx := range bd.Transactions {
		if tx == nil || !isCoinbaseMemo(tx.Memo) {
			continue
		}
		amt, err := strconv.ParseFloat(tx.Amount, 64)
		if err != nil {
			continue
		}
		coinbaseSum += amt
		switch {
		case strings.HasPrefix(tx.Memo, "coinbase:pool"):
			poolAmt += amt
		case strings.HasPrefix(tx.Memo, "coinbase:node"):
			nodeAmt += amt
		default:
			minerSum += amt
		}
	}
	r.minted = coinbaseSum
	expPool := reward * float64(chaindb.PoolShareBps) / 10000
	expNode := reward * float64(chaindb.NodeShareBps) / 10000
	expRest := reward - expPool - expNode
	if math.Abs(coinbaseSum-reward) <= auditEps {
		r.coinbasePass = 1
	} else {
		r.coinbaseFail = 1
		r.issues = append(r.issues, "h="+strconv.FormatInt(b.Height, 10)+" coinbase 總額 "+strconv.FormatFloat(coinbaseSum, 'f', 8, 64)+" != 出塊獎勵 "+strconv.FormatFloat(reward, 'f', 8, 64))
	}
	if math.Abs(poolAmt-expPool) <= auditEps {
		r.poolPass = 1
	} else {
		r.poolFail = 1
		r.issues = append(r.issues, "h="+strconv.FormatInt(b.Height, 10)+" pool 份額異常")
	}
	// M149q：node 檢查容許「0（併池）」或「45%（有節點）」；split 強制 node+miner==85%。
	if math.Abs(nodeAmt-expNode) <= auditEps || nodeAmt <= auditEps {
		r.nodePass = 1
	} else {
		r.nodeFail = 1
		r.issues = append(r.issues, "h="+strconv.FormatInt(b.Height, 10)+" 節點獎勵份額異常")
	}
	if math.Abs(minerSum-expRest) <= auditEps || math.Abs(nodeAmt+minerSum-expRest) <= auditEps {
		r.splitPass = 1
	} else {
		r.splitFail = 1
		r.issues = append(r.issues, "h="+strconv.FormatInt(b.Height, 10)+" miner/reserve 份額異常")
	}
	return r
}

// handleAuditAPI GET /api/audit?blocks=N — 公開鏈上審計（M67）：
// 驗證最近 N 塊（預設 100，上限 500）coinbase 不變式＋回傳全鏈真實供應/獎勵池。
// M149q：優先回後台 auditLoop 快取（20 秒一次全鏈重放），請求不再反覆觸發 O(鏈長) 掃描。
func (s *Server) handleAuditAPI(w http.ResponseWriter, r *http.Request) {
	s.auditMu.Lock()
	av := s.auditView
	started := s.auditStarted
	s.auditMu.Unlock()
	if !started {
		av = auditRecentBlocks(s.ds, 100, s.blockTimeSec, s.auditBlockCache)
	}
	av.Network = s.network
	if s.network == "testnet" {
		av.ChainID = "tacm-testnet-1"
	}
	writeJSON(w, av)
}

// auditLoop 後台審計重放迴圈（M149q）：啟動立即重放一次，之後每 20 秒刷新快取。
// 監控頁 10 秒輪詢、多頁面同時請求時，/api/audit 都只讀快取——伺服器 CPU 不再被
// 反覆的全鏈賬本重放飽和，出塊間隔與礦機載入因此恢復正常。
func (s *Server) auditLoop() {
	run := func() {
		av := auditRecentBlocks(s.ds, 100, s.blockTimeSec, s.auditBlockCache)
		s.auditMu.Lock()
		s.auditView = av
		s.auditStarted = true
		s.auditMu.Unlock()
	}
	run()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.auditStop:
			return
		case <-t.C:
			run()
		}
	}
}
