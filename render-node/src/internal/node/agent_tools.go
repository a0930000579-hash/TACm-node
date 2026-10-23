package node

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"tacm/internal/ai"
	"tacm/internal/chaindb"
)

// agentMemory 跨請求的 Agent 對話記憶（package-level，避免改 RPCServer 結構；mutex 保護）。
var (
	agentMemoryOnce sync.Once
	agentMemory     *ai.MemoryStore
)

func sharedAgentMemory() *ai.MemoryStore {
	agentMemoryOnce.Do(func() { agentMemory = ai.NewMemoryStore() })
	return agentMemory
}

// v163：把 TACm 既有服務包成 Agent 工具（ai.Registry）。
// 嚴禁重寫業務邏輯——唯讀工具直接呼叫 s.node.db / oracle / dex / defi 既有查詢；
// 寫操作工具標 RequiresConfirm，由 Agent 層把關（未 confirm 不執行 Run，亦即不簽/不廣播任何交易）。

// buildAgentRegistry 組裝本節點的 Agent 工具表。memReader 提供 read_memory 工具所需對話記憶。
func (s *RPCServer) buildAgentRegistry(memReader func() string) *ai.Registry {
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

	// --- 寫操作工具（RequiresConfirm：未 confirm 不執行）---
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
			// confirm=true 才會執行至此。用 Quote 做唯讀估算，回報預估輸出。
			addr, _ := args["address"].(string)
			amount, _ := args["amount"].(string)
			amt, _ := new(big.Int).SetString(strings.TrimPrefix(amount, "0"), 10)
			if amt == nil {
				amt = big.NewInt(0)
			}
			estimate := "execution requires your wallet signature on the DApp; this action is prepared once you confirm."
			if s.node.dex != nil && amt.Sign() > 0 {
				if pools, err := s.node.dex.Pools(); err == nil && len(pools) > 0 {
					if _, out, qerr := s.node.dex.Quote(pools[0].ID, "TAC", amt); qerr == nil {
						estimate = fmt.Sprintf("Estimated output via pool %s: %s (quote only; final swap executes after wallet signature).", pools[0].ID, out.String())
					}
				}
			}
			return ai.ToolResult{
				Summary: fmt.Sprintf("Dex swap prepared for %s pair=%s amount=%s. %s", addr, args["pair"], amount, estimate),
				Data:    map[string]any{"prepared": true, "address": addr, "pair": args["pair"], "amount": amount},
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
			addr, _ := args["address"].(string)
			return ai.ToolResult{
				Summary: fmt.Sprintf("DeFi deposit prepared for %s asset=%s amount=%s. Execution requires wallet signature and your confirmation.",
					addr, args["asset"], args["amount"]),
				Data: map[string]any{"prepared": true, "address": addr, "asset": args["asset"], "amount": args["amount"]},
			}, nil
		},
	})

	reg.MustRegister(&ai.Tool{
		Name:            "defi_borrow",
		Description:     "Prepare a DeFi borrow (CDP). Requires confirmation before execution.",
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
			addr, _ := args["address"].(string)
			return ai.ToolResult{
				Summary: fmt.Sprintf("DeFi borrow prepared for %s asset=%s amount=%s. CDP liquidation risk applies; execution requires wallet signature.",
					addr, args["asset"], args["amount"]),
				Data: map[string]any{"prepared": true, "address": addr, "asset": args["asset"], "amount": args["amount"]},
			}, nil
		},
	})

	return reg
}

// 確保 chaindb import 被使用（未來擴充查詢型別時保留）。
var _ = chaindb.RewardPoolAddr
