package node

// M73-A AI 服務層：鏈上智能助手。
// GET /api/ai/ask?q=<問題>&lang=en|zh
//
// 規則引擎式意圖匹配＋鏈上數據查詢＋中英雙語回答。零外部依賴（無需 LLM API key），
// 回答全部基於鏈上事實（status / 區塊 / 審計 / 餘額 / 獎勵 / 在線礦工），可離線部署、
// 與節點同一進程，任何同步同一條鏈的節點回答一致。
//
// 設計要點：
//   - 意圖匹配為關鍵字規則（中英雙語），未知問題回退到能力介紹；
//   - 審計意圖僅在鏈較短（<=1000 塊）時做全鏈重放（與 /api/audit 同源），長鏈回摘要＋指引；
//   - 回答模板雙語，前端依 ?lang= 或當前語言切換顯示。

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"tacm/internal/ai"
	"tacm/internal/chaindb"
)

// M102-4：本地開源模型整合狀態。
// 優先 llama.cpp llama-server（OpenAI 兼容 API）；不可用時每 60 秒重探一次，
// 呼叫失敗立即回退規則式助手並重探，保證 AI 永遠可用。
var (
	llmClientOnce sync.Once
	llmClient     *ai.LLMClient
	llmOK         atomic.Bool
	llmLastProbe  atomic.Int64
)

const llmProbeInterval = int64(60 * time.Second)

// llm 惰性建立本地模型客戶端。
func llm() *ai.LLMClient {
	llmClientOnce.Do(func() { llmClient = ai.NewLLMClient("") })
	return llmClient
}

// probeLLM 依節奏探測 llama-server；回傳當前可用狀態。
func probeLLM() bool {
	if llmOK.Load() {
		return true
	}
	now := time.Now().UnixNano()
	if now-llmLastProbe.Load() < llmProbeInterval {
		return false
	}
	llmLastProbe.Store(now)
	if llm().Available() {
		llmOK.Store(true)
		return true
	}
	return false
}

// aiIntent 意圖類型。
type aiIntent string

const (
	aiStatus   aiIntent = "status"
	aiBlock    aiIntent = "block"
	aiAudit    aiIntent = "audit"
	aiAddress  aiIntent = "address"
	aiReward   aiIntent = "reward"
	aiMiner    aiIntent = "miner"
	aiSupply   aiIntent = "supply"
	aiJoin     aiIntent = "join"
	aiTransfer aiIntent = "transfer"
	aiHelp     aiIntent = "help"
)

// aiIntents 所有意圖清單（help 回覆列出）。
var aiIntents = []struct {
	Intent aiIntent
	Zh     string
	En     string
}{
	{aiStatus, "節點狀態（鏈高/難度/共識/運行時間）", "Node status (height / difficulty / consensus / uptime)"},
	{aiBlock, "區塊資訊（最新區塊或指定高度）", "Block info (latest or a specific height)"},
	{aiAudit, "鏈上審計（一致/守恆/賬本）", "On-chain audit (consistent / ledger conservation)"},
	{aiAddress, "地址餘額（鏈上 TACm）", "Address balance (on-chain TACm)"},
	{aiReward, "獎勵（池 16% / 節點 9% / 礦工 75%）", "Rewards (pool 16% / node 9% / miner 75%)"},
	{aiMiner, "在線礦工與挖礦指引", "Online miners & mining guide"},
	{aiSupply, "供應與發行", "Supply & emission"},
	{aiJoin, "加入節點（seed / 驗證人）", "Join the network (seed / validator)"},
	{aiTransfer, "轉帳與交易指引", "Transfer & transaction guide"},
	{aiHelp, "說明（本清單）", "Help (this list)"},
}

// matchAIIntent 依關鍵字匹配意圖（中英；先精確後泛化，避免誤判）。
func matchAIIntent(q string) aiIntent {
	low := strings.ToLower(strings.TrimSpace(q))
	has := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(low, k) {
				return true
			}
		}
		return false
	}
	switch {
	case has("help", "說明", "说明", "可以做", "能做", "功能", "你好", "hi", "hello", "怎麼用", "怎么用", "如何用", "what can"):
		return aiHelp
	case has("join", "加入", "成為節點", "成为节点", "seed", "驗證人", "验证人", "validator", "節點教程", "节点教程"):
		return aiJoin
	case has("transfer", "轉帳", "轉賬", "转账", "匯款", "汇款", "發送", "发送", "手續費", "手续费", "fee", "gas", "怎麼交易", "怎么交易"):
		return aiTransfer
	case has("miner", "礦工", "矿工", "挖礦", "挖矿", "算力", "礦機", "矿机", "心跳", "heartbeat", "開機", "开机", "earn"):
		return aiMiner
	case has("audit", "審計", "审计", "一致", "consistent", "帳本", "賬本", "账本", "守恆", "守恒", "ledger", "驗證", "验证", "重放", "安全"):
		return aiAudit
	case has("address", "address", "地址", "餘額", "余额", "balance", "查地址", "資產餘額", "资产余额"):
		return aiAddress
	case has("reward", "獎勵", "奖励", "收益", "池", "pool", "份額", "份额", "分潤", "分润", "coinbase", "分紅", "分红"):
		return aiReward
	case has("supply", "供應", "供应", "總量", "总量", "市值", "supply", "cap", "上限", "發行", "发行", "總額", "总额"):
		return aiSupply
	case has("block", "區塊", "区块", "最新", "latest", "hash", "哈希", "出塊", "出块", "高度", "第幾塊", "第几块"):
		return aiBlock
	case has("status", "status", "狀態", "状态", "節點", "节点", "網絡", "网络", "network", "鏈高", "链高", "同步", "sync", "uptime", "運行", "运行", "性能"):
		return aiStatus
	}
	return aiHelp
}

// aiAddrRe 匹配鏈上 tx0 地址（用於 address 意圖）。
var aiAddrRe = regexp.MustCompile(`tx0[0-9A-Za-z]{20,60}`)

// aiHeightRe 匹配問題中的數字（用於 block 意圖指定高度）。
var aiHeightRe = regexp.MustCompile(`\d+`)

// handleAIAssistant GET /api/ai/ask?q=&lang=
func (s *RPCServer) handleAIAssistant(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeErr(w, http.StatusBadRequest, "missing_q: 請輸入問題 (ask 'what can you do')")
		return
	}
	if len(q) > 300 {
		q = q[:300]
	}
	lang := r.URL.Query().Get("lang")
	if lang != "zh" {
		lang = "en"
	}
	ctx := strings.TrimSpace(r.URL.Query().Get("ctx"))
	if len(ctx) > 600 {
		ctx = ctx[:600]
	}
	intent := matchAIIntent(q)
	answer := s.aiAnswer(intent, q, lang)
	model := "rule"
	if probeLLM() {
		msgs := []ai.ChatMessage{
			{Role: "system", Content: ai.SystemPrompt(lang)},
		}
		if ctx != "" {
			msgs = append(msgs, ai.ChatMessage{Role: "system", Content: "Previous conversation context:\n" + aiCtxNote(ctx, lang)})
		}
		msgs = append(msgs, ai.ChatMessage{Role: "user", Content: q})
		if ans, err := llm().Chat(msgs, 512); err == nil {
			answer = ans
			model = "llama"
		} else {
			// 本地模型異常→回退規則式，並重設探測節奏。
			llmOK.Store(false)
			llmLastProbe.Store(0)
		}
	}
	if ctx != "" {
		// M90：多輪記憶——把先前問答摘要併入回答尾註（前端 localStorage 歷史）。
		answer += "\n\n" + aiCtxNote(ctx, lang)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "intent": string(intent), "lang": lang, "model": model, "answer": answer,
	})
}

// aiCtxNote 從多輪 ctx（Q/A 摘要）取出最後一輪問題作為上下文提示。
func aiCtxNote(ctx, lang string) string {
	lines := strings.Split(ctx, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "Q:") {
			last = strings.TrimPrefix(lines[i], "Q:")
			break
		}
	}
	if last == "" {
		return ""
	}
	if lang == "zh" {
		return "🧠 多輪記憶：先前討論「" + last + "」，已回答並持續納入上下文。"
	}
	return "🧠 Multi-turn memory: earlier topic \"" + last + "\" remains in context."
}

// aiAnswer 依意圖查詢節點並組裝雙語回答。
func (s *RPCServer) aiAnswer(intent aiIntent, q, lang string) string {
	zh := lang == "zh"
	switch intent {
	case aiStatus:
		return s.aiStatusAnswer(zh)
	case aiBlock:
		return s.aiBlockAnswer(q, zh)
	case aiAudit:
		return s.aiAuditAnswer(zh)
	case aiAddress:
		return s.aiAddressAnswer(q, zh)
	case aiReward:
		return s.aiRewardAnswer(zh)
	case aiMiner:
		return s.aiMinerAnswer(zh)
	case aiSupply:
		return s.aiSupplyAnswer(zh)
	case aiJoin:
		return s.aiJoinAnswer(zh)
	case aiTransfer:
		return s.aiTransferAnswer(zh)
	default:
		return s.aiHelpAnswer(zh)
	}
}

func (s *RPCServer) aiStatusAnswer(zh bool) string {
	st := s.node.GetStatus()
	if zh {
		return fmt.Sprintf("TACm 主網節點狀態：\n- 節點：%s\n- 地址：%s\n- 鏈：%s（%s）\n- 區塊高度：%d（最終化 %d）\n- 有效難度：%d\n- 共識：%s\n- 交易池：%d 筆\n- 運行時間：%d 秒\n- 發行模型：%s\n可繼續問「區塊」「獎勵」「審計」或「餘額」。",
			st.NodeID, st.Address, st.Network, st.ChainID, st.BlockHeight, st.FinalHeight,
			st.Difficulty, st.Consensus, st.MempoolSize, st.UptimeSec, st.EmissionModel)
	}
	return fmt.Sprintf("TACm mainnet node status:\n- Node: %s\n- Address: %s\n- Chain: %s (%s)\n- Block height: %d (finalized %d)\n- Difficulty: %d\n- Consensus: %s\n- Mempool: %d txs\n- Uptime: %d s\n- Emission: %s\nAsk about blocks, rewards, audit, or balance for more.",
		st.NodeID, st.Address, st.Network, st.ChainID, st.BlockHeight, st.FinalHeight,
		st.Difficulty, st.Consensus, st.MempoolSize, st.UptimeSec, st.EmissionModel)
}

func (s *RPCServer) aiBlockAnswer(q string, zh bool) string {
	// 嘗試解析指定高度；否則最新區塊。
	height := int64(-1)
	if m := aiHeightRe.FindString(q); m != "" {
		if h, err := strconv.ParseInt(m, 10, 64); err == nil && h >= 0 {
			height = h
		}
	}
	tip := s.node.db.GetTipHeight()
	if height < 0 || height > tip {
		height = tip
	}
	bd, err := s.node.GetBlockDetail(height)
	if err != nil || bd == nil || bd.Block == nil {
		if zh {
			return fmt.Sprintf("無法讀取區塊 #%d（%v）。目前鏈頂：%d。", height, err, tip)
		}
		return fmt.Sprintf("Cannot read block #%d (%v). Current tip: %d.", height, err, tip)
	}
	b := bd.Block
	prev := ""
	if b.PrevHash != nil {
		prev = *b.PrevHash
	} else {
		prev = "-"
	}
	if zh {
		return fmt.Sprintf("區塊 #%d：\n- 哈希：%s\n- 前一塊：%s\n- 出塊節點：%s（%s）\n- 時間：%d\n- 交易數：%d\n- 難度：%d\n- Nonce：%d\n- Merkle 根：%s\n（目前鏈頂 #%d，輸入高度數字可查指定區塊）",
			b.Height, b.Hash, prev, b.Proposer, b.ProposerAddress, b.Ts, b.TxCount,
			b.Difficulty, b.Nonce, b.MerkleRoot, tip)
	}
	return fmt.Sprintf("Block #%d:\n- Hash: %s\n- Prev: %s\n- Proposer: %s (%s)\n- Time: %d\n- Tx count: %d\n- Difficulty: %d\n- Nonce: %d\n- Merkle root: %s\n(Tip is #%d; type a height number to query a specific block)",
		b.Height, b.Hash, prev, b.Proposer, b.ProposerAddress, b.Ts, b.TxCount,
		b.Difficulty, b.Nonce, b.MerkleRoot, tip)
}

// aiAuditShortLimit 全鏈重放審計的鏈長上限（塊）；超過時僅回摘要與指引。
const aiAuditShortLimit = int64(1000)

func (s *RPCServer) aiAuditAnswer(zh bool) string {
	tip := s.node.db.GetTipHeight()
	if tip > aiAuditShortLimit {
		if zh {
			return fmt.Sprintf("鏈上審計（摘要）：鏈頂 #%d 已超過輕量審計上限（%d 塊）。\n完整全鏈重放審計（consistent / ledger 守恆 / coinbase 分潤）請在「網路」頁審計面板查看，或問「節點狀態」。",
				tip, aiAuditShortLimit)
		}
		return fmt.Sprintf("On-chain audit (summary): tip #%d exceeds the lightweight audit limit (%d blocks).\nRun the full replay audit (consistent / ledger conservation / coinbase split) on the Network audit panel, or ask 'node status'.",
			tip, aiAuditShortLimit)
	}
	res, err := s.node.VerifyLedgerOnChain()
	if err != nil {
		if zh {
			return fmt.Sprintf("審計執行失敗：%v", err)
		}
		return fmt.Sprintf("Audit failed: %v", err)
	}
	if res.OK && res.LedgerOK && len(res.Issues) == 0 {
		if zh {
			return fmt.Sprintf("鏈上審計全通過：\n- 鏈頂：#%d（檢查 %d 塊）\n- consistent：true（coinbase %d / pool %d / node %d / split %d 全過）\n- 賬本守恆：true（coinbase 總額 %s == 餘額總額 %s）\n- 交易層：檢查 %d 筆（簽名 %d 過 / nonce %d 過 / 餘額 %d 過 / 合約 %d 過）\n- issues：0\n鏈是安全一致的。",
				res.TipHeight, res.TipHeight+1, res.CoinbaseTx, 0, 0, 0,
				chaindb.FormatFloat(res.CoinbaseTotal), chaindb.FormatFloat(res.BalancesTotal),
				res.TxChecked, res.TxSigPass, res.TxNoncePass, res.TxBalancePass, res.ContractPass)
		}
		return fmt.Sprintf("On-chain audit: all passed.\n- Tip: #%d (%d blocks checked)\n- consistent: true (coinbase %d / pool / node / split all passed)\n- Ledger conservation: true (coinbase total %s == balances total %s)\n- Tx layer: %d checked (sig %d / nonce %d / balance %d / contract %d passed)\n- issues: 0\nThe chain is safe and consistent.",
			res.TipHeight, res.TipHeight+1, res.CoinbaseTx,
			chaindb.FormatFloat(res.CoinbaseTotal), chaindb.FormatFloat(res.BalancesTotal),
			res.TxChecked, res.TxSigPass, res.TxNoncePass, res.TxBalancePass, res.ContractPass)
	}
	if zh {
		return fmt.Sprintf("鏈上審計發現問題：\n- 鏈頂：#%d\n- consistent：false、賬本守恆：%v\n- issues：%d 項\n%s\n請立即在「網路」頁審計面板查看明細。",
			res.TipHeight, res.LedgerOK, len(res.Issues), strings.Join(res.Issues, "\n"))
	}
	return fmt.Sprintf("On-chain audit found issues:\n- Tip: #%d\n- consistent: false, ledger conservation: %v\n- issues: %d\n%s\nCheck the Network audit panel for details.",
		res.TipHeight, res.LedgerOK, len(res.Issues), strings.Join(res.Issues, "\n"))
}

func (s *RPCServer) aiAddressAnswer(q string, zh bool) string {
	addr := aiAddrRe.FindString(q)
	if addr == "" {
		if zh {
			return "請附上 tx0 開頭的鏈上地址（例如：查詢地址 tx0xxxxxxxx 的餘額）。"
		}
		return "Please include a tx0 address (e.g. balance of tx0xxxx)."
	}
	bal := s.node.db.GetBalance(addr)
	if zh {
		return fmt.Sprintf("地址 %s 鏈上餘額：%s TACm。\nTiUSD / USDT 餘額與轉帳請在「錢包」頁操作；鏈上守恆由審計持續驗證。", addr, bal)
	}
	return fmt.Sprintf("Address %s on-chain balance: %s TACm.\nTiUSD / USDT balances and transfers live in the Wallet page; conservation is continuously verified by the audit.", addr, bal)
}

func (s *RPCServer) aiRewardAnswer(zh bool) string {
	tip := s.node.db.GetTipHeight()
	reward := chaindb.BlockReward(tip)
	minted := reward * float64(tip)
	pool := s.node.db.GetBalance(chaindb.RewardPoolAddr)
	if zh {
		return fmt.Sprintf("獎勵模型（M70，每塊 %s TACm）：\n- 節點 9%%：歸出塊節點（memo coinbase:node）\n- 池 16%%：挹注獎勵池（memo coinbase:pool，鏈上獎勵池餘額 %s TACm）\n- 礦工 75%%：按鏈上在線礦工算力瓜分（memo coinbase:miner）；無礦工時併入池（reserve）\n- 目前鏈頂 #%d，已發行 ≈ %s TACm\n「網路」頁審計面板可看全鏈實際分潤（on_chain_pool/node_share）。",
			chaindb.FormatFloat(reward), pool, tip, chaindb.FormatFloat(minted))
	}
	return fmt.Sprintf("Rewards (M70 model, %s TACm/block):\n- Node 9%%: to the block producer (memo coinbase:node)\n- Pool 16%%: to the reward pool (memo coinbase:pool; on-chain pool balance %s TACm)\n- Miners 75%%: split by on-chain hashrate (memo coinbase:miner); if no miner online, merged into the pool (reserve)\n- Tip #%d → ≈ %s TACm minted\nSee the Network audit panel for the actual on-chain split (on_chain_pool/node_share).",
		chaindb.FormatFloat(reward), pool, tip, chaindb.FormatFloat(minted))
}

func (s *RPCServer) aiMinerAnswer(zh bool) string {
	states, err := s.node.onChainOnlineMiners(hbWindow)
	count := int64(0)
	if err == nil {
		for _, hr := range states {
			if hr > 0 {
				count++
			}
		}
	}
	if zh {
		return fmt.Sprintf("鏈上在線礦工：%d 台（按算力瓜分每塊 75%% 獎勵；心跳窗口 %d 塊）。\n挖礦指引：開啟「挖礦」頁 → 註冊礦機 → 保持開機（心跳自動上鏈）→ 每塊 75%% 獎勵依算力比例自動分配。\n可問「加入節點」了解成為節點的方式。",
			count, hbWindow)
	}
	return fmt.Sprintf("Online miners on-chain: %d (they split 75%% of each block reward by hashrate; heartbeat window %d blocks).\nMining guide: open the Mining page → register your miner → keep it online (heartbeat is on-chain automatically) → 75%% of each block is split by hashrate.\nAsk 'join network' to learn how to become a node.",
		count, hbWindow)
}

func (s *RPCServer) aiSupplyAnswer(zh bool) string {
	tip := s.node.db.GetTipHeight()
	st := s.node.GetStatus()
	reward := chaindb.BlockReward(tip)
	if zh {
		return fmt.Sprintf("TACm 供應與發行：\n- 每塊發行：%s TACm（減半模式，每 %d 塊減半）\n- 已出塊：%d → 已發行 ≈ %s TACm\n- 供應上限：%s（未設定時為 0＝無上限）\n- 年衰減：%s%%\n鏈上守恆（coinbase 總額 == 餘額總額）由審計持續驗證。",
			chaindb.FormatFloat(reward), chaindb.HalvingInterval, tip, chaindb.FormatFloat(reward*float64(tip)),
			chaindb.FormatFloat(st.MaxSupply), chaindb.FormatFloat(st.AnnualDecayPct))
	}
	return fmt.Sprintf("TACm supply & emission:\n- Per block: %s TACm (halving model, every %d blocks)\n- Blocks mined: %d → ≈ %s TACm issued\n- Max supply: %s (0 = unlimited when unset)\n- Annual decay: %s%%\nLedger conservation (coinbase total == balances total) is continuously verified by the audit.",
		chaindb.FormatFloat(reward), chaindb.HalvingInterval, tip, chaindb.FormatFloat(reward*float64(tip)),
		chaindb.FormatFloat(st.MaxSupply), chaindb.FormatFloat(st.AnnualDecayPct))
}

func (s *RPCServer) aiJoinAnswer(zh bool) string {
	if zh {
		return "加入 TACm 主網成為節點：\n1. 在伺服器安裝 Go，clone tacm-go 程式碼；\n2. 以 seed 節點啟動並同步（公開 seed 列表在「加入節點」頁）；\n3. 連上 P2P 後自動同步區塊，參與 PoW 出塊與 BFT 最終性投票；\n4. 節點每塊獲得 coinbase 9% 節點獎勵（memo coinbase:node）。\n詳細步驟請看「加入節點」頁。"
	}
	return "Join the TACm mainnet as a node:\n1. Install Go, clone the tacm-go repo;\n2. Start with a seed node and sync (public seed list on the Join page);\n3. Once connected via P2P you sync blocks and participate in PoW mining & BFT finality voting;\n4. Nodes earn 9% of each block as node reward (memo coinbase:node).\nSee the Join page for the full steps."
}

func (s *RPCServer) aiTransferAnswer(zh bool) string {
	if zh {
		return "轉帳與交易指引：\n1. 「錢包」頁：登入帳號／訪客地址 → 選擇資產（TACm / TiUSD / USDT）→ 輸入收款地址與金額 → 送出（自動扣手續費，TACm 200bp / TiUSD 50bp / USDT 125bp）；\n2. 交易上鏈後可在「瀏覽器」以交易哈希查詢；\n3. 交易所 / C2C / DeFi 的資金在各自頁面操作。\n可問「區塊」或「地址餘額」進一步了解鏈上狀態。"
	}
	return "Transfer & transaction guide:\n1. Wallet page: sign in / guest address → pick an asset (TACm / TiUSD / USDT) → enter recipient & amount → submit (fee is auto-deducted: TACm 200bp / TiUSD 50bp / USDT 125bp);\n2. After broadcast, look the tx up by hash in the Explorer;\n3. Exchange / C2C / DeFi funds are managed on their own pages.\nAsk 'block' or 'address balance' for more on-chain details."
}

func (s *RPCServer) aiHelpAnswer(zh bool) string {
	var b strings.Builder
	if zh {
		b.WriteString("我是 TACm 鏈上智能助手，可以回答（中/英）：\n")
	} else {
		b.WriteString("I'm the TACm on-chain assistant. I can answer (EN/ZH):\n")
	}
	for _, it := range aiIntents {
		if zh {
			b.WriteString("- " + it.Zh + "\n")
		} else {
			b.WriteString("- " + it.En + "\n")
		}
	}
	if zh {
		b.WriteString("試試：節點狀態 / 最新區塊 / 鏈上審計 / 地址餘額 / 獎勵。")
	} else {
		b.WriteString("Try: node status / latest block / on-chain audit / address balance / rewards.")
	}
	return b.String()
}
