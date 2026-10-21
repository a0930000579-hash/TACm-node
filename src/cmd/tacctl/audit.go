// tacctl audit —— 鏈上審計器（M66）：全鏈驗證 coinbase 獎勵瓜分一致性與鏈結構，
// 直接回應「任何入口看到的獎勵/總產出必須代表鏈上真實」的驗收標準。
// 核心不變式：每區塊 coinbase 交易總額 == BlockReward(height)（12% 進獎勵池、
// 88% 按算力分給在線礦工或全數保留）——coinbase 是唯一增發源，逐塊驗證即證明
// 「A+B 不可能大於真實總額」。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/chaindb"
)

// nodeStatusResp 對應節點 HTTP /status 回應（欄位名與節點協議一致：block_height 等）。
type nodeStatusResp struct {
	NodeID             string `json:"node_id"`
	Address            string `json:"address"`
	Network            string `json:"network"`
	ChainID            string `json:"chain_id"`
	BlockHeight        int64  `json:"block_height"`
	FinalBlockHeight   int64  `json:"final_block_height"`
	MempoolSize        int    `json:"mempool_size"`
	EffectiveDifficulty int   `json:"effective_difficulty"`
	UptimeSec          int64  `json:"uptime_sec"`
	Consensus          string `json:"consensus"`
}

// txResp 對應區塊內交易的 JSON（chaindb.Transaction）。
type txResp struct {
	TxHash   string `json:"tx_hash"`
	FromAddr string `json:"from"`
	ToAddr   string `json:"to"`
	Amount   string `json:"amount"`
	Fee      string `json:"fee"`
	Memo     string `json:"memo"`
}

// blockDetailResp 對應 /api/block/{height} 完整回應（chaindb.BlockDetail 嵌入
// *chaindb.Block，JSON 為區塊欄位平鋪＋transactions 陣列）。
type blockDetailResp struct {
	Height          int64    `json:"height"`
	Hash            string   `json:"hash"`
	PrevHash        string   `json:"prev_hash"`
	ProposerAddress string   `json:"proposer_address,omitempty"`
	Ts              int64    `json:"ts"`
	Difficulty      int      `json:"difficulty"`
	TxCount         int      `json:"tx_count"`
	Transactions    []txResp `json:"transactions"`
}

// blockAudit 單塊 coinbase 審計結果。
type blockAudit struct {
	Height      int64
	Reward      float64
	CoinbaseSum float64 // 本塊 coinbase 交易總額
	PoolAmt     float64 // coinbase:pool 份額
	MinerSum    float64 // coinbase:miner / coinbase:reserve 份額
	CoinbaseOK  bool    // 總額 == BlockReward
	PoolOK      bool    // pool 份額 == 12%
	SplitOK     bool    // miner/reserve 總額 == 88%
	OK          bool
	Issues      []string
}

// AuditResult 全鏈審計報告（記憶存檔點：M66 核心結構）。
type AuditResult struct {
	ChainID      string  `json:"chain_id"`
	NodeID       string  `json:"node_id"`
	TipHeight    int64   `json:"tip_height"`
	BlockCount   int64   `json:"block_count"`
	CoinbasePass int64   `json:"coinbase_pass"` // coinbase 總額 == BlockReward 的塊數
	CoinbaseFail int64   `json:"coinbase_fail"`
	PoolPass     int64   `json:"pool_pass"` // pool 份額 == 12% 的塊數
	PoolFail     int64   `json:"pool_fail"`
	SplitPass    int64   `json:"split_pass"` // miner/reserve 總額 == 88% 的塊數
	SplitFail    int64   `json:"split_fail"`
	TotalMinted  float64 `json:"total_minted"` // 累計鏈上增發（height>=1 coinbase 總額）
	StructureOK  bool    `json:"structure_ok"`
	Issues       []string `json:"issues"`
	Passed       bool    `json:"passed"`
}

const auditEps = 1e-6 // 浮點容忍：金額以小數字串精確存儲，計算誤差遠小於此。

// isCoinbase 判斷交易是否為 coinbase 增發（M58/M59 瓜分明細格式）。
func isCoinbase(memo string) bool {
	return memo == "coinbase" || strings.HasPrefix(memo, "coinbase:")
}

// auditBlock 檢查單塊：coinbase 總額 == BlockReward；pool == 12%；miner/reserve == 88%。
// 創世塊（height<=0）由 genesis 配置，不適用 coinbase 瓜分規則，直接跳過（OK=true）。
func auditBlock(h int64, txs []txResp) blockAudit {
	a := blockAudit{Height: h, CoinbaseOK: true, PoolOK: true, SplitOK: true, OK: true}
	if h <= 0 {
		return a
	}
	a.Reward = chaindb.BlockReward(h)
	for _, t := range txs {
		if !isCoinbase(t.Memo) {
			continue
		}
		amt := parseAmt(t.Amount)
		a.CoinbaseSum += amt
		switch t.Memo {
		case "coinbase:pool":
			a.PoolAmt += amt
		case "coinbase:reserve":
			a.MinerSum += amt // reserve 屬 88% 份額
		default: // "coinbase" 或 "coinbase:miner"
			a.MinerSum += amt
		}
	}
	if math.Abs(a.CoinbaseSum-a.Reward) > auditEps {
		a.CoinbaseOK = false
		a.Issues = append(a.Issues, fmt.Sprintf("h=%d coinbase 總額 %.8f != BlockReward %.8f", h, a.CoinbaseSum, a.Reward))
	}
	if math.Abs(a.PoolAmt-a.Reward*chaindb.PoolShareBps/10000) > auditEps {
		a.PoolOK = false
		a.Issues = append(a.Issues, fmt.Sprintf("h=%d pool 份額 %.8f != 12%s (%.8f)", h, a.PoolAmt, "%", a.Reward*chaindb.PoolShareBps/10000))
	}
	if math.Abs(a.MinerSum-a.Reward*(1-chaindb.PoolShareBps/10000.0)) > auditEps {
		a.SplitOK = false
		a.Issues = append(a.Issues, fmt.Sprintf("h=%d miner/reserve 份額 %.8f != 88%s (%.8f)", h, a.MinerSum, "%", a.Reward*(1-chaindb.PoolShareBps/10000.0)))
	}
	a.OK = a.CoinbaseOK && a.PoolOK && a.SplitOK
	return a
}

// auditChain 審計整條鏈：以 fetchBlock 拉取每塊（可注入替身供測試），回傳 AuditResult。
func auditChain(st nodeStatusResp, fetchBlock func(h int64) (*blockDetailResp, error)) AuditResult {
	res := AuditResult{ChainID: st.ChainID, NodeID: st.NodeID, TipHeight: st.BlockHeight, StructureOK: true}
	prevTs := int64(0)
	first := true
	for h := int64(0); h <= st.BlockHeight; h++ {
		bd, err := fetchBlock(h)
		if err != nil {
			res.StructureOK = false
			res.Issues = append(res.Issues, fmt.Sprintf("h=%d 讀取失敗: %v", h, err))
			res.Passed = false
			continue
		}
		if bd == nil {
			res.StructureOK = false
			res.Issues = append(res.Issues, fmt.Sprintf("h=%d 區塊缺失（鏈不連續）", h))
			res.Passed = false
			continue
		}
		res.BlockCount++
		if bd.Height != h {
			res.StructureOK = false
			res.Issues = append(res.Issues, fmt.Sprintf("h=%d 回傳高度 %d（回應錯位）", h, bd.Height))
			res.Passed = false
		}
		if first {
			prevTs = bd.Ts
			first = false
		} else if bd.Ts < prevTs {
			res.StructureOK = false
			res.Issues = append(res.Issues, fmt.Sprintf("h=%d 時間戳倒退 (%d < %d)", h, bd.Ts, prevTs))
		} else {
			prevTs = bd.Ts
		}
		// 創世塊（h=0）難度可為 0（genesis 配置），不適用出塊難度規則。
		if h > 0 && bd.Difficulty < 1 {
			res.StructureOK = false
			res.Issues = append(res.Issues, fmt.Sprintf("h=%d 難度非法 %d", h, bd.Difficulty))
		}
		ba := auditBlock(h, bd.Transactions)
		if h > 0 {
			res.TotalMinted += ba.CoinbaseSum
		}
		if ba.CoinbaseOK {
			res.CoinbasePass++
		} else {
			res.CoinbaseFail++
			res.Issues = append(res.Issues, ba.Issues...)
		}
		if ba.PoolOK {
			res.PoolPass++
		} else {
			res.PoolFail++
		}
		if ba.SplitOK {
			res.SplitPass++
		} else {
			res.SplitFail++
		}
	}
	res.Passed = res.CoinbaseFail == 0 && res.StructureOK
	return res
}

// parseAmt 解析金額字串（chaindb.FormatFloat 格式）。
func parseAmt(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// cmdAudit tacctl audit -node URL —— 全鏈審計入口。
func cmdAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	node := fs.String("node", "http://127.0.0.1:8332", "節點 RPC URL")
	fs.Parse(args)

	client := &http.Client{Timeout: 15 * time.Second}
	st, err := getNodeStatus(client, *node)
	if err != nil {
		return fmt.Errorf("讀取節點狀態: %w", err)
	}
	fetchBlock := func(h int64) (*blockDetailResp, error) {
		return getBlock(client, *node, h)
	}
	res := auditChain(st, fetchBlock)

	// 輸出審計報告。
	fmt.Println("=== TAC 鏈上審計報告 ===")
	fmt.Printf("節點   : %s（%s）\n", *node, st.NodeID)
	fmt.Printf("鏈 ID  : %s\n", res.ChainID)
	fmt.Printf("鏈高   : %d\n", res.TipHeight)
	fmt.Printf("區塊數 : %d\n", res.BlockCount)
	fmt.Println("── coinbase 瓜分一致性 ──")
	fmt.Printf("✓ coinbase 總額 == BlockReward：%d 塊通過 / %d 異常\n", res.CoinbasePass, res.CoinbaseFail)
	fmt.Printf("✓ pool 份額 == %s：%d 塊通過 / %d 異常\n", "12%", res.PoolPass, res.PoolFail)
	fmt.Printf("✓ miner/reserve 總額 == %s：%d 塊通過 / %d 異常\n", "88%", res.SplitPass, res.SplitFail)
	fmt.Println("── 鏈結構 ──")
	fmt.Printf("✓ 高度連續 / 時間戳遞增 / 難度合法：%v\n", res.StructureOK)
	fmt.Println("── 供應 ──")
	fmt.Printf("累計鏈上增發（height>=1 coinbase）: %.8f TACm\n", res.TotalMinted)
	if len(res.Issues) > 0 {
		fmt.Println("── 異常清單 ──")
		for _, is := range res.Issues {
			fmt.Println("✗", is)
		}
		fmt.Println("結論: ✗ 發現異常 —— 鏈上獎勵不一致，需修復後再審計")
		return fmt.Errorf("審計未通過（%d 項異常）", len(res.Issues))
	}
	fmt.Println("結論: ✓ 全部檢查通過 —— 每塊 coinbase 嚴格等於出塊獎勵，無超發/少發，鏈上供應與獎勵一致")
	return nil
}

// getNodeStatus 呼叫 GET /status。
func getNodeStatus(client *http.Client, node string) (nodeStatusResp, error) {
	var st nodeStatusResp
	resp, err := client.Get(node + "/status")
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&st)
	return st, err
}

// getBlock 呼叫 GET /api/block/{height}（web 同埠模式中 /block/{h} 為 HTML 瀏覽器頁，
// JSON 由 /api/block/{h} 提供，含完整交易與 coinbase 瓜分明細）。
func getBlock(client *http.Client, node string, h int64) (*blockDetailResp, error) {
	resp, err := client.Get(fmt.Sprintf("%s/api/block/%d", node, h))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("block HTTP %d: %s", resp.StatusCode, string(b))
	}
	var bd blockDetailResp
	if err := json.NewDecoder(resp.Body).Decode(&bd); err != nil {
		return nil, err
	}
	return &bd, nil
}
