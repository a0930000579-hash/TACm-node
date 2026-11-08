package node

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"tacm/internal/ai"
	"tacm/internal/chaindb"
	"tacm/internal/defi"
	"tacm/internal/wallet"
)

// agentMemory 跨請求的 Agent 對話記憶（package-level，避免改 RPCServer 結構；mutex 保護）。
var (
	agentMemoryMu  sync.Mutex
	agentMemory    *ai.MemoryStore
	agentMemoryDir string
)

// sharedAgentMemory 回傳全程序共享的記憶存放；首次以 dataDir 建立持久化版本，
// 重啟後可從磁碟延續各 session 歷史。dataDir 為空時退回純記憶體。
func sharedAgentMemory(dataDir string) *ai.MemoryStore {
	agentMemoryMu.Lock()
	defer agentMemoryMu.Unlock()
	if agentMemory != nil && agentMemoryDir == dataDir {
		return agentMemory
	}
	if agentMemory == nil {
		dir := ""
		if dataDir != "" {
			dir = filepath.Join(dataDir, "agent-memory")
		}
		mem, err := ai.NewPersistentMemoryStore(dir)
		if err != nil {
			log.Printf("agent memory: persistent store init failed (%v), falling back to in-memory", err)
			mem = ai.NewMemoryStore()
		}
		agentMemory = mem
		agentMemoryDir = dataDir
	}
	return agentMemory
}

// v165：聯網搜尋 provider（免金鑰，SearXNG 類端點由 TAC_SEARCH_URL 設定；未設定＝永遠降級）。
var (
	searchProviderOnce sync.Once
	searchProvider     ai.SearchProvider
)

func sharedSearchProvider() ai.SearchProvider {
	searchProviderOnce.Do(func() { searchProvider = ai.NewHTTPSearchProviderFromEnv() })
	return searchProvider
}

// withLinkedAddr 工具層 backstop：模型 tool_call 未帶 address 時，以本請求連結地址補上。
// 優先序：模型明確給的 address > 連結地址。僅補預設值，不影響寫操作 needs_confirm 閘與簽名。
func withLinkedAddr(args map[string]any, linked string) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	if a, _ := args["address"].(string); strings.TrimSpace(a) == "" && linked != "" {
		args["address"] = linked
	}
	return args
}

// mustStr 安全取出工具參數中的字串（缺失或型別不符回空字串）。
func mustStr(v any) string {
	s, _ := v.(string)
	return s
}

// v163：把 TACm 既有服務包成 Agent 工具（ai.Registry）。
// 嚴禁重寫業務邏輯——唯讀工具直接呼叫 s.node.db / oracle / dex / defi 既有查詢；
// 寫操作工具標 RequiresConfirm，由 Agent 層把關（未 confirm 不執行 Run，亦即不簽/不廣播任何交易）。

// buildAgentRegistry 組裝本節點的 Agent 工具表。memReader 提供 read_memory 工具所需對話記憶。
// linkedAddr 為本請求連結的錢包地址，做為工具層 backstop。
func (s *RPCServer) buildAgentRegistry(memReader func() string, linkedAddr string) *ai.Registry {
	linkedAddr = strings.TrimSpace(linkedAddr)
	reg := ai.NewRegistry()

	// --- 唯讀工具 ---
	reg.MustRegister(&ai.Tool{
		Name:        "get_balance",
		Description: "Get the on-chain TACm balance of an address.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"address"},
			"properties": map[string]any{
				"address": map[string]any{"type": "string", "description": "0x wallet address"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			args = withLinkedAddr(args, linkedAddr)
			addr, _ := args["address"].(string)
			addr = strings.TrimSpace(addr)
			if addr == "" {
				return ai.ToolResult{}, fmt.Errorf("address is required")
			}
			bal := s.node.db.GetBalance(addr)
			return ai.ToolResult{
				Summary: fmt.Sprintf("Address %s balance: %s TACm", addr, bal),
				Data:    map[string]any{"address": addr, "balance": bal},
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:        "get_price",
		Description: "Get the current market price of a symbol (e.g. TAC/USDT) from the on-chain oracle.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"symbol": map[string]any{"type": "string", "description": "market symbol like TAC/USDT"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			if s.node.oracleSvc == nil {
				return ai.ToolResult{}, fmt.Errorf("oracle unavailable")
			}
			sym, _ := args["symbol"].(string)
			if sym == "" {
				sym = "TAC/USDT"
			}
			p, ok := s.node.oracleSvc.Get(sym)
			if !ok {
				// 回傳快照摘要供模型參考。
				snap := s.node.oracleSvc.Snapshot()
				names := make([]string, 0, len(snap))
				for _, it := range snap {
					names = append(names, fmt.Sprintf("%s=%.4f", it.Symbol, it.Price))
				}
				return ai.ToolResult{
					Summary: fmt.Sprintf("No quote for %s. Available: %s", sym, strings.Join(names, ", ")),
					Data:    map[string]any{"symbol": sym, "available": names},
				}, nil
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("%s price: %.4f USDT (24h %.2f%%, source %s)", p.Symbol, p.Price, p.Chg24h, p.Source),
				Data:    p,
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:        "get_recent_txs",
		Description: "List recent on-chain transactions for an address.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"address"},
			"properties": map[string]any{
				"address": map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			args = withLinkedAddr(args, linkedAddr)
			addr, _ := args["address"].(string)
			addr = strings.TrimSpace(addr)
			if addr == "" {
				return ai.ToolResult{}, fmt.Errorf("address is required")
			}
			txs, err := s.node.db.GetTransactionsByAddress(addr, 10)
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("query txs: %w", err)
			}
			short := make([]map[string]any, 0, len(txs))
			for _, tx := range txs {
				short = append(short, map[string]any{
					"hash":   tx.TxHash,
					"from":   tx.FromAddr,
					"to":     tx.ToAddr,
					"amount": tx.Amount,
					"height": tx.BlockHeight,
					"status": tx.Status,
				})
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("%d recent transactions for %s", len(short), addr),
				Data:    short,
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:        "get_pool",
		Description: "List DEX liquidity pools and reserves.",
		Parameters:  map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			if s.node.dex == nil {
				return ai.ToolResult{}, fmt.Errorf("dex engine unavailable")
			}
			pools, err := s.node.dex.Pools()
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("list pools: %w", err)
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("%d DEX pools", len(pools)),
				Data:    pools,
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:        "read_memory",
		Description: "Read this conversation's recent history/memory.",
		Parameters:  map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			if memReader == nil {
				return ai.ToolResult{Summary: "no memory available"}, nil
			}
			return ai.ToolResult{Summary: memReader()}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:        "get_tx",
		Description: "Get the status/receipt of a transaction by its hash (mempool vs confirmed block).",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"tx_hash"},
			"properties": map[string]any{
				"tx_hash": map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			hash, _ := args["tx_hash"].(string)
			hash = strings.TrimSpace(hash)
			if hash == "" {
				return ai.ToolResult{}, fmt.Errorf("tx_hash is required")
			}
			tx, err := s.node.db.GetTransaction(hash)
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("query tx: %w", err)
			}
			if tx == nil {
				return ai.ToolResult{
					Summary: fmt.Sprintf("Transaction %s not found yet (still propagating or unknown hash).", hash),
					Data:    map[string]any{"tx_hash": hash, "found": false},
				}, nil
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("Tx %s: status=%s from=%s to=%s amount=%s height=%d",
					hash, tx.Status, tx.FromAddr, tx.ToAddr, tx.Amount, tx.BlockHeight),
				Data: map[string]any{
					"tx_hash": hash, "found": true, "status": tx.Status,
					"from": tx.FromAddr, "to": tx.ToAddr, "amount": tx.Amount,
					"height": tx.BlockHeight,
				},
			}, nil
		},
	})

	// --- 寫操作工具（RequiresConfirm：未 confirm 不執行）---
	reg.MustRegister(&ai.Tool{
		Name:            "transfer",
		Description:     "Execute an on-chain/internal-ledger transfer of TACm, TiUSD or USDT to another address. Requires confirmation.",
		RequiresConfirm: true,
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"from", "to", "asset", "amount"},
			"properties": map[string]any{
				"from":   map[string]any{"type": "string"},
				"to":     map[string]any{"type": "string"},
				"asset":  map[string]any{"type": "string", "description": "TACm | TiUSD | USDT"},
				"amount": map[string]any{"type": "string", "description": "amount as decimal string"},
				"memo":   map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			if s.node.walletSvc == nil {
				return ai.ToolResult{}, fmt.Errorf("wallet service unavailable")
			}
			from, _ := args["from"].(string)
			to, _ := args["to"].(string)
			assetRaw, _ := args["asset"].(string)
			asset := normalizeAsset(assetRaw)
			amountStr, _ := args["amount"].(string)
			memo, _ := args["memo"].(string)
			from = strings.TrimSpace(from)
			to = strings.TrimSpace(to)
			if from == "" {
				from = linkedAddr
			}
			if from == "" || to == "" {
				return ai.ToolResult{}, fmt.Errorf("from and to are required")
			}
			amt, err := wallet.NewAmount(amountStr, wallet.Asset(asset))
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("invalid amount: %w", err)
			}
			fee, err := s.node.walletSvc.Transfer(from, to, wallet.Asset(asset), amt, memo)
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("transfer failed: %w", err)
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("Transferred %s %s from %s to %s (fee %s %s).",
					amountStr, asset, from, to, fee.String(wallet.Asset(asset)), asset),
				Data: map[string]any{
					"executed": true, "from": from, "to": to, "asset": asset,
					"amount": amountStr, "fee": fee.String(wallet.Asset(asset)),
				},
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:            "dex_swap",
		Description:     "Prepare a DEX swap (tokenIn -> tokenOut). Requires confirmation before execution.",
		RequiresConfirm: true,
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"address", "pair", "amount"},
			"properties": map[string]any{
				"address": map[string]any{"type": "string"},
				"pair":    map[string]any{"type": "string", "description": "e.g. TAC/USDT"},
				"amount":  map[string]any{"type": "string", "description": "input amount as decimal string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			// confirm=true 才會執行至此：實際呼叫 DEX 引擎完成兌換（與 POST /dex/swap 同一路徑）。
			if s.node.dex == nil {
				return ai.ToolResult{}, fmt.Errorf("dex engine unavailable")
			}
			args = withLinkedAddr(args, linkedAddr)
			addr, _ := args["address"].(string)
			amount, _ := args["amount"].(string)
			amt, ok := new(big.Int).SetString(strings.TrimSpace(amount), 10)
			if !ok || amt.Sign() <= 0 {
				return ai.ToolResult{}, fmt.Errorf("invalid or non-positive amount: %q", amount)
			}
			pools, err := s.node.dex.Pools()
			if err != nil || len(pools) == 0 {
				return ai.ToolResult{}, fmt.Errorf("no DEX pool available")
			}
			poolID := pools[0].ID
			tokenIn := "TAC"
			if p, _ := args["pair"].(string); p != "" {
				up := strings.ToUpper(p)
				if strings.HasPrefix(up, "TIUSD") || strings.HasPrefix(up, "USDT") {
					tokenIn = strings.SplitN(up, "/", 2)[0]
				}
			}
			tokenOut, out, err := s.node.dex.Swap(poolID, s.node.nodeAddress, tokenIn, amt, nil)
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("swap failed: %w", err)
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("Swap executed for %s on pool %s: %s %s -> %s %s.",
					addr, poolID, amount, tokenIn, out.String(), tokenOut),
				Data: map[string]any{
					"executed": true, "address": addr, "pool": poolID,
					"token_in": tokenIn, "amount_in": amt.String(),
					"token_out": tokenOut, "amount_out": out.String(),
				},
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:            "defi_deposit",
		Description:     "Prepare a DeFi deposit. Requires confirmation before execution.",
		RequiresConfirm: true,
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"address", "asset", "amount"},
			"properties": map[string]any{
				"address": map[string]any{"type": "string"},
				"asset":   map[string]any{"type": "string"},
				"amount":  map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			// confirm=true 才會執行至此：與 POST /api/defi/deposit 同一路徑（轉帳入池＋記錄，失敗退款）。
			ds, ok := s.defiSvc()
			if !ok || s.node.walletSvc == nil {
				return ai.ToolResult{}, fmt.Errorf("defi service unavailable")
			}
			args = withLinkedAddr(args, linkedAddr)
			addr, _ := args["address"].(string)
			addr = strings.TrimSpace(addr)
			asset := normalizeAsset(mustStr(args["asset"]))
			amount, err := strconv.ParseFloat(strings.TrimSpace(mustStr(args["amount"])), 64)
			if err != nil || amount <= 0 {
				return ai.ToolResult{}, fmt.Errorf("invalid amount: %v", args["amount"])
			}
			if addr == "" {
				return ai.ToolResult{}, fmt.Errorf("address is required")
			}
			if _, err := s.node.walletSvc.Transfer(addr, defi.LendingAccount, wallet.Asset(asset), amountOf(amount, asset), "defi:lending:deposit"); err != nil {
				return ai.ToolResult{}, fmt.Errorf("deposit transfer failed: %w", err)
			}
			if err := ds.DepositLending(addr, asset, amount); err != nil {
				_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, addr, wallet.Asset(asset), amountOf(amount, asset), "defi:refund")
				return ai.ToolResult{}, fmt.Errorf("deposit record failed: %w", err)
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("DeFi deposit executed: %s deposited %f %s into the lending market.", addr, amount, asset),
				Data: map[string]any{
					"executed": true, "address": addr, "asset": asset, "amount": amount,
				},
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:            "defi_borrow",
		Description:     "Execute a DeFi borrow: lock collateral and borrow another asset from the lending market. Requires confirmation.",
		RequiresConfirm: true,
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"address", "collateral_asset", "collateral_amount", "borrow_asset", "borrow_amount"},
			"properties": map[string]any{
				"address":           map[string]any{"type": "string"},
				"collateral_asset":  map[string]any{"type": "string"},
				"collateral_amount": map[string]any{"type": "string"},
				"borrow_asset":      map[string]any{"type": "string"},
				"borrow_amount":     map[string]any{"type": "string"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			// confirm=true 才會執行至此：與 POST /api/defi/borrow 同一路徑（抵押入池＋開倉＋放款，失敗退款）。
			ds, ok := s.defiSvc()
			if !ok || s.node.walletSvc == nil {
				return ai.ToolResult{}, fmt.Errorf("defi service unavailable")
			}
			args = withLinkedAddr(args, linkedAddr)
			addr, _ := args["address"].(string)
			addr = strings.TrimSpace(addr)
			collAsset := normalizeAsset(mustStr(args["collateral_asset"]))
			borrowAsset := normalizeAsset(mustStr(args["borrow_asset"]))
			collAmount, err := strconv.ParseFloat(strings.TrimSpace(mustStr(args["collateral_amount"])), 64)
			if err != nil || collAmount <= 0 {
				return ai.ToolResult{}, fmt.Errorf("invalid collateral_amount: %v", args["collateral_amount"])
			}
			borrowAmount, err := strconv.ParseFloat(strings.TrimSpace(mustStr(args["borrow_amount"])), 64)
			if err != nil || borrowAmount <= 0 {
				return ai.ToolResult{}, fmt.Errorf("invalid borrow_amount: %v", args["borrow_amount"])
			}
			if addr == "" {
				return ai.ToolResult{}, fmt.Errorf("address is required")
			}
			if _, err := s.node.walletSvc.Transfer(addr, defi.LendingAccount, wallet.Asset(collAsset), amountOf(collAmount, collAsset), "defi:borrow:collateral"); err != nil {
				return ai.ToolResult{}, fmt.Errorf("collateral transfer failed: %w", err)
			}
			loanID, err := ds.Borrow(addr, collAsset, collAmount, borrowAsset, borrowAmount)
			if err != nil {
				_ = s.node.walletSvc.InternalTransfer(defi.LendingAccount, addr, wallet.Asset(collAsset), amountOf(collAmount, collAsset), "defi:refund")
				return ai.ToolResult{}, fmt.Errorf("borrow failed: %w", err)
			}
			if err := s.node.walletSvc.InternalTransfer(defi.LendingAccount, addr, wallet.Asset(borrowAsset), amountOf(borrowAmount, borrowAsset), fmt.Sprintf("defi:borrow:%d", loanID)); err != nil {
				return ai.ToolResult{}, fmt.Errorf("loan disbursement failed: %w", err)
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("Borrow executed (loan #%d): %s locked %f %s collateral and received %f %s.",
					loanID, addr, collAmount, collAsset, borrowAmount, borrowAsset),
				Data: map[string]any{
					"executed": true, "address": addr, "loan_id": loanID,
					"collateral_asset": collAsset, "collateral_amount": collAmount,
					"borrow_asset": borrowAsset, "borrow_amount": borrowAmount,
				},
			}, nil
		},
	})

	// --- v165：聯網搜尋 grounding（唯讀，不需確認閘） ---
	reg.MustRegister(&ai.Tool{
		Name:        "web_search",
		Description: "Search the web for LATEST/time-sensitive info (news, current events, macro/global markets, unknown facts). Use ONLY when the user needs up-to-date information; not for static knowledge, chit-chat, or TACm on-chain data. Returns titles/URLs/snippets. Chinese queries are recalled in zh+english and merged.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"query"},
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "search query"},
				"lang":  map[string]any{"type": "string", "description": "hint language: zh | en"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			query, _ := args["query"].(string)
			query = strings.TrimSpace(query)
			if query == "" {
				return ai.ToolResult{}, fmt.Errorf("query is required")
			}
			lang, _ := args["lang"].(string)
			if lang == "" {
				lang = "en"
			}
			out, err := ai.RunSearch(ctx, sharedSearchProvider(), query, lang)
			if err != nil {
				// 誠實降級：不回錯誤以讓迴圈繼續，明確告知無法連線、不編造來源（跟隨語言）。
				return ai.ToolResult{
					Summary: ai.WebUnavailableNote(lang),
					Data:    map[string]any{"queries": []string{query}, "citations": []ai.SearchResult{}},
				}, nil
			}
			// 摘要：列出前幾筆標題＋網址（給模型；前端另從 citations 渲染；跟隨語言）。
			var b strings.Builder
			if lang == "zh" {
				fmt.Fprintf(&b, "召回 %d 筆結果，查詢：%s。\n", len(out.Results), strings.Join(out.Queries, "；"))
			} else {
				fmt.Fprintf(&b, "Recalled %d result(s) via queries: %s.\n", len(out.Results), strings.Join(out.Queries, "; "))
			}
			for i, r := range out.Results {
				if i >= 5 {
					break
				}
				fmt.Fprintf(&b, "- %s\n  %s\n  %s\n", r.Title, r.URL, r.Snippet)
			}
			return ai.ToolResult{
				Summary: b.String(),
				Data:    map[string]any{"queries": out.Queries, "citations": out.Results},
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:        "web_fetch",
		Description: "Fetch and extract the main text body of a specific URL returned by web_search. Use only when a snippet is not enough; caps length and strips HTML.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"url"},
			"properties": map[string]any{
				"url": map[string]any{"type": "string", "description": "http(s) URL to fetch"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			u, _ := args["url"].(string)
			u = strings.TrimSpace(u)
			if u == "" {
				return ai.ToolResult{}, fmt.Errorf("url is required")
			}
			text, err := sharedSearchProvider().Fetch(ctx, u)
			if err != nil {
				return ai.ToolResult{}, fmt.Errorf("fetch failed: %w", err)
			}
			return ai.ToolResult{
				Summary: text,
				Data:    map[string]any{"url": u},
			}, nil
		},
	})

	// create_file：把 AI 生成的內容封裝成可下載檔案（記憶體暫存，附下載 URL）。
	reg.MustRegister(&ai.Tool{
		Name:        "create_file",
		Description: "Create a downloadable file from generated content (code, text, data). Returns a download URL.",
		Parameters: map[string]any{
			"type":     "object",
			"required": []any{"filename", "content"},
			"properties": map[string]any{
				"filename":  map[string]any{"type": "string", "description": "Target file name (extension optional)"},
				"language":  map[string]any{"type": "string", "description": "Language/format hint (go, python, javascript, html, json, markdown, text)"},
				"content":   map[string]any{"type": "string", "description": "Full file content"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (ai.ToolResult, error) {
			filename := sanitizeFilename(strings.TrimSpace(mustStr(args["filename"])))
			if filename == "" {
				return ai.ToolResult{}, fmt.Errorf("filename is required")
			}
			language := strings.ToLower(strings.TrimSpace(mustStr(args["language"])))
			filename = ensureExtension(filename, language)
			mime, language := mimeForFile(filename, language)
			content := mustStr(args["content"])
			f, err := s.getFileStore().Put(filename, language, mime, []byte(content))
			if err != nil {
				return ai.ToolResult{}, err
			}
			downloadURL := "/api/ai/file/" + f.ID
			return ai.ToolResult{
				Summary: fmt.Sprintf("File %s ready (%d bytes). Download: %s", f.Filename, len(f.Content), downloadURL),
				Data: map[string]any{
					"file_id": f.ID, "filename": f.Filename, "language": f.Language,
					"mime": f.MIME, "size": len(f.Content), "download_url": downloadURL,
				},
			}, nil
		},
	})

	return reg
}

// 確保 chaindb import 被使用（未來擴充查詢型別時保留）。
var _ = chaindb.RewardPoolAddr
