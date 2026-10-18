package node

// heartbeat.go — M60：礦工開機/關機/心跳「上鏈」。
//
// 問題：M58/M59 之後，coinbase 瓜分明細已上鏈（follower 照單入帳），但「誰開機」仍是
// 各節點本地 SQL（RegisterMiner/SetActiveMiner）。follower 端按礦機開機只寫自己本地表，
// 錨點出塊時看不到 → 鏈上瓜分沒有該礦工 → 「節點上按礦機沒反應」、任何入口看到的人數不一致。
//
// 解法：把「開機/關機/心跳」做成鏈上交易（hb 交易）：
//   - 開機：節點以自身金鑰代簽一筆 from=節點、to=礦工、amount=0、fee=0、
//     memo=`hb:on:<hashrate>` 的交易，廣播入 mempool 並上鏈；
//   - 關機：memo=`hb:off`；
//   - 過期：鏈上在線窗口（hbWindow 塊）內最後一筆 hb 為 on 且未 off 視為在線，超出窗口自動下線。
//
// 出塊者的 coinbase 瓜分與 web 顯示全部改讀「鏈上 heartbeat 視角」：任何節點只要同步了
// 同一條鏈，看到的在線礦工、算力與瓜分完全一致 → 錨點/節點不再有入口差異。

import (
	"log"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"tacm/internal/crypto"
)

// hbMemoPrefix 礦工心跳 memo 前綴；格式：hb:on:<hashrate> 或 hb:off。
const hbMemoPrefix = "hb:"

// hbWindow 鏈上在線窗口（塊）：窗口內最後一筆 hb 為 on 且未 off 視為在線；超出窗口自動下線。
const hbWindow = int64(60)

// hbRefresh 伺服器端心跳續命間隔：每 30s 對已開機礦工續發 hb:on（窗口 60 塊 ≈ 120s，保留 2 倍餘量）。
const hbRefresh = 30 * time.Second

// parseHeartbeatMemo 解析 hb:on:<hashrate> / hb:off（hashrate 可省略＝0）。
// 非 hb 交易回傳 ok=false。
func parseHeartbeatMemo(memo string) (on bool, hashrate int64, ok bool) {
	if !strings.HasPrefix(memo, hbMemoPrefix) {
		return false, 0, false
	}
	parts := strings.Split(memo, ":")
	if len(parts) < 2 {
		return false, 0, false
	}
	switch parts[1] {
	case "on":
		on = true
	case "off":
		on = false
	default:
		return false, 0, false
	}
	if len(parts) > 2 {
		hashrate, _ = strconv.ParseInt(parts[2], 10, 64)
	}
	return on, hashrate, true
}

// buildHeartbeatTx 構造礦工在線/離線交易（from=節點、to=礦工、0 金額/手續費、
// memo=hb:on:<hashrate>|hb:off），以節點金鑰簽名。返回未提交的交易 map。
func (n *Node) buildHeartbeatTx(minerAddr string, on bool, hashrate int64) (map[string]any, error) {
	if n.keypair == nil {
		return nil, errors.New("node: 節點金鑰不可用")
	}
	if !crypto.IsValidAddress(minerAddr) {
		return nil, errors.New("node: 非法礦工地址")
	}
	from, err := n.keypair.Address()
	if err != nil {
		return nil, err
	}
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	memo := hbMemoPrefix + "off"
	if on {
		memo = fmt.Sprintf("%son:%d", hbMemoPrefix, hashrate)
	}
	tx := map[string]any{
		"from": from, "to": minerAddr,
		"amount": "0", "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(n.keypair.PublicKeyCompressed()),
		"memo":   memo,
	}
	sig, err := crypto.SignTransaction(tx, n.keypair.PrivateKey())
	if err != nil {
		return nil, fmt.Errorf("node: 簽名心跳交易: %w", err)
	}
	tx["signature"] = sig
	return tx, nil
}

// M149m-7：節點在線心跳——節點（含 follower）定期發 hb:on:0 心跳，計入鏈上
// 在線節點集，依時間權證領取 45% 節點獎勵（不再只有出塊節點獨享）。
func (n *Node) StartNodeHeartbeat(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		// M149m-9：節點啟動自動 PoT 公鑰註冊（免費、冪等——已註冊則跳過）。
		// v51：註冊失敗不再只試一次——每個心跳週期檢查，未註冊即重試，
		// 直到成功（避免節點啟動時主鏈/mempool 未就緒導致永久無法註冊）。
		ensureRegistered := func() bool {
			regs, err := n.db.GetNodeRegistry()
			if err != nil {
				return false
			}
			for i := range regs {
				if regs[i].Address == n.nodeAddress {
					return true
				}
			}
			if _, err := n.RegisterAsPotNode(); err != nil {
				log.Printf("[node-hb] PoT 節點註冊失敗（將重試）: %v", err)
				return false
			}
			log.Printf("[node-hb] PoT 節點註冊交易已提交")
			return true
		}
		ensureRegistered()
		if _, err := n.SubmitHeartbeat(n.nodeAddress, true, 0); err != nil {
			log.Printf("[node-hb] 初始心跳失敗: %v", err)
		}
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !ensureRegistered() {
					// 註冊未成功時不續發心跳——心跳沒有註冊就沒有獎勵意義，
					// 且註冊失敗多半伴隨交易通道異常，避免重複卡 nonce。
					continue
				}
				if _, err := n.SubmitHeartbeat(n.nodeAddress, true, 0); err != nil {
					log.Printf("[node-hb] 心跳失敗: %v", err)
				}
			}
		}
	}()
}

// SubmitHeartbeat 提交礦工在線/離線交易（入 mempool + P2P 廣播）；返回交易哈希。
// 這是 M60「開機狀態上鏈」的統一入口：任何節點（含 follower）開機/關機礦機時呼叫。
func (n *Node) SubmitHeartbeat(minerAddr string, on bool, hashrate int64) (string, error) {
	tx, err := n.buildHeartbeatTx(minerAddr, on, hashrate)
	if err != nil {
		return "", err
	}
	return n.SubmitTransaction(tx)
}

// hbCache* M145：鏈上在線礦工視窗快取（3 秒 TTL）。/api/miners 每次輪詢都全掃
// 60 塊交易，手機端明顯卡頓（「卡很久不顯示」）；同 tip 下短 TTL 快取讓輪詢秒回應。
var (
	hbCacheMu  sync.Mutex
	hbCache    map[string]int64
	hbCacheTip int64
	hbCacheAt  time.Time
)

// onChainOnlineMiners 掃鏈頂回溯 window 塊的 hb 交易，回傳「鏈上在線礦工視角」：
// {礦工地址: 算力}。每礦工以窗口內最後一筆 hb 為準（on＝在線+算力；off＝下線）。
// 掃描按區塊高度與交易序遞增，天然保持時間順序；任何節點同步同一條鏈結果一致。
// M145：同 tip 且 3 秒內重複查詢直接回快取（副本），tip 推進後自動重掃。
func (n *Node) onChainOnlineMiners(window int64) (map[string]int64, error) {
	tip := n.db.GetTipHeight()
	hbCacheMu.Lock()
	defer hbCacheMu.Unlock()
	if hbCache != nil && hbCacheTip == tip && time.Since(hbCacheAt) < 3*time.Second {
		out := make(map[string]int64, len(hbCache))
		for k, v := range hbCache {
			out[k] = v
		}
		return out, nil
	}
	start := tip - window + 1
	if start < 1 {
		start = 1
	}
	states := map[string]int64{}
	for h := start; h <= tip; h++ {
		txs, err := n.db.GetTransactionsByBlock(h)
		if err != nil {
			return nil, err
		}
		for _, tx := range txs {
			on, hr, ok := parseHeartbeatMemo(tx.Memo)
			if !ok {
				continue
			}
			if on {
				states[tx.ToAddr] = hr
			} else {
				delete(states, tx.ToAddr)
			}
		}
	}
	hbCache = states
	hbCacheTip = tip
	hbCacheAt = time.Now()
	return states, nil
}

// OnChainMiners 對外暴露鏈上在線礦工視角（供 web 層渲染）：回傳 (地址清單, 總算力)。
// 取代原本「掃 coinbase 收款人×5M 標稱算力」的近似視角：hb 交易攜帶真實算力。
func (n *Node) OnChainMiners(window int64) ([]string, int64, error) {
	states, err := n.onChainOnlineMiners(window)
	if err != nil {
		return nil, 0, err
	}
	addrs := make([]string, 0, len(states))
	var total int64
	for addr, hr := range states {
		if hr > 0 {
			addrs = append(addrs, addr)
			total += hr
		}
	}
	return addrs, total, nil
}

// submitActiveHeartbeats 對所有已「開機」礦工續發鏈上心跳（minerHeartbeatLoop 每 30s 呼叫）。
// 離開頁面仍持續挖礦：本地 active=1 的礦工由節點伺服器代為續發 hb:on，保持鏈上在線。
func (n *Node) submitActiveHeartbeats() {
	if n.walletSvc == nil {
		return
	}
	active, err := n.walletSvc.Store().ActiveMiners()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[miner-heartbeat] active: %v\n", err)
		return
	}
	for _, a := range active {
		hr, err := n.walletSvc.Store().MinerHashrate(a)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[miner-heartbeat] hr %s: %v\n", a, err)
			continue
		}
		if _, err := n.SubmitHeartbeat(a, true, hr); err != nil {
			fmt.Fprintf(os.Stderr, "[miner-heartbeat] submit %s: %v\n", a, err)
			continue
		}
		if err := n.walletSvc.Store().TickMiner(a); err != nil {
			fmt.Fprintf(os.Stderr, "[miner-heartbeat] tick %s: %v\n", a, err)
		}
	}
}
