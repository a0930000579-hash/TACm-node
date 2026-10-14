package p2p

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"tacm/internal/bridge"
	"tacm/internal/chaindb"
	"tacm/internal/consensus/bft"
)

const defaultTTL = 2

// BroadcastBlock 在節點出塊/接入塊後向全網廣播區塊；自身產生的塊先標記已見防回環。
func (n *Network) BroadcastBlock(b *chaindb.Block, txs []chaindb.Transaction) {
	if b != nil {
		n.markSeenBlock(b.Hash)
	}
	n.flood(gossipReq{
		Kind: "block", TTL: defaultTTL, From: n.cfg.SelfID,
		Block: b, BlockTxs: txs,
	})
}

// BroadcastTx 在節點接收交易後向全網廣播。
func (n *Network) BroadcastTx(tx map[string]any) {
	if h, ok := tx["tx_hash"].(string); ok && h != "" {
		n.markSeenTx(h)
	}
	n.flood(gossipReq{
		Kind: "tx", TTL: defaultTTL, From: n.cfg.SelfID, Tx: tx,
	})
}

// BroadcastTxs M120：批量交易廣播——一次 flood 攜帶多筆交易，
// 消除批量提交（SubmitTransactions）後逐筆 HTTP 廣播的往返成本。
// 接收端逐筆去重/驗證/入池（receiveTxs），行為與單筆 BroadcastTx 等價。
func (n *Network) BroadcastTxs(txs []map[string]any) {
	if len(txs) == 0 {
		return
	}
	for _, tx := range txs {
		if h, ok := tx["tx_hash"].(string); ok && h != "" {
			n.markSeenTx(h)
		}
	}
	n.flood(gossipReq{
		Kind: "txs", TTL: defaultTTL, From: n.cfg.SelfID, Txs: txs,
	})
}

// receiveBlock 處理網絡傳來的區塊：去重 → 交節點驗證接入 → 遞減 TTL 轉發。
func (n *Network) receiveBlock(b *chaindb.Block, txs []chaindb.Transaction, ttl int, from string) {
	if b == nil || b.Hash == "" {
		return
	}
	if !n.markSeenBlock(b.Hash) {
		return // 已見，去重
	}
	if err := n.host.OnIncomingBlock(b, txs); err != nil {
		log.Printf("[p2p] 接入區塊 h=%d 失敗: %v", b.Height, err)
		// M86：收到缺口/超前區塊 → 立即觸發一次批量同步追塊（不等下一個 tick），
		// 根治 follower 晚加入或 gossip 跳塊時的長時間停擺。
		if strings.Contains(err.Error(), "區塊缺口") {
			go n.syncOnce()
		}
	}
	if ttl > 0 {
		n.flood(gossipReq{
			Kind: "block", TTL: ttl - 1, From: from,
			Block: b, BlockTxs: txs,
		})
	}
}

// receiveTx 處理網絡傳來的交易：去重 → 交節點 → 轉發。
func (n *Network) receiveTx(tx map[string]any, ttl int, from string) {
	h, _ := tx["tx_hash"].(string)
	if h == "" {
		return
	}
	if !n.markSeenTx(h) {
		return
	}
	_ = n.host.OnIncomingTx(tx)
	if ttl > 0 {
		n.flood(gossipReq{Kind: "tx", TTL: ttl - 1, From: from, Tx: tx})
	}
}

// receiveTxs M120：處理網絡傳來的批量交易：逐筆去重 → 交節點（OnIncomingTx，
// 含 M118 寬鬆入池語義）→ TTL 內一次轉發（保留批次，避免再次逐筆往返）。
func (n *Network) receiveTxs(txs []map[string]any, ttl int, from string) {
	fwd := make([]map[string]any, 0, len(txs))
	for _, tx := range txs {
		h, _ := tx["tx_hash"].(string)
		if h == "" {
			continue
		}
		if !n.markSeenTx(h) {
			continue
		}
		_ = n.host.OnIncomingTx(tx)
		fwd = append(fwd, tx)
	}
	if ttl > 0 && len(fwd) > 0 {
		n.flood(gossipReq{Kind: "txs", TTL: ttl - 1, From: from, Txs: fwd})
	}
}

// flood 把消息並發 POST 給除原始來源外的所有在線節點（回環靠 seen 去重）。
func (n *Network) flood(req gossipReq) {
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	for _, p := range n.onlinePeers() {
		if p.NodeID == req.From {
			continue
		}
		go n.post(p.BaseURL+"/p2p/gossip", body)
	}
}

// floodAll 向所有已知節點（含尚未心跳確認的）發送，用於不可丟失的跨鏈消息：
// 一次錯過即永久丟失的提案/簽名不應被在線狀態過濾。
func (n *Network) floodAll(req gossipReq) {
	body, err := json.Marshal(req)
	if err != nil {
		return
	}
	for _, p := range n.snapshotPeers() {
		if p.NodeID == req.From {
			continue
		}
		go n.post(p.BaseURL+"/p2p/gossip", body)
	}
}

func (n *Network) post(url string, body []byte) {
	if n.ctx == nil {
		return
	}
	httpReq, err := http.NewRequestWithContext(n.ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(httpReq)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func (n *Network) markSeenBlock(h string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seenBlocks[h]; ok {
		return false
	}
	n.seenBlocks[h] = struct{}{}
	return true
}

func (n *Network) markSeenTx(h string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seenTxs[h]; ok {
		return false
	}
	n.seenTxs[h] = struct{}{}
	return true
}

// BroadcastVote 廣播本節點產生的共識投票（prevote/precommit）。
func (n *Network) BroadcastVote(v *bft.Vote) {
	if v == nil {
		return
	}
	n.markSeenVote(voteSeenKey(v))
	n.flood(gossipReq{
		Kind: "consensus", TTL: defaultTTL, From: n.cfg.SelfID, Vote: v,
	})
}

// receiveVote 處理網絡傳來的投票：去重 → 交節點驗證聚合 → 遞減 TTL 轉發。
func (n *Network) receiveVote(v *bft.Vote, ttl int, from string) {
	if v == nil {
		return
	}
	if !n.markSeenVote(voteSeenKey(v)) {
		return
	}
	if err := n.host.OnIncomingVote(v); err != nil {
		log.Printf("[p2p] 接收投票失敗: %v", err)
	}
	if ttl > 0 {
		n.flood(gossipReq{
			Kind: "consensus", TTL: ttl - 1, From: from, Vote: v,
		})
	}
}

func (n *Network) markSeenVote(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seenVotes[key]; ok {
		return false
	}
	n.seenVotes[key] = struct{}{}
	return true
}

func voteSeenKey(v *bft.Vote) string {
	bh := ""
	if v.BlockHash != nil {
		bh = *v.BlockHash
	}
	return fmt.Sprintf("%s:%d:%d:%s:%s",
		v.Type, v.Height, v.Round, v.Validator, bh)
}

// BroadcastViewChange 廣播提議超時後的輪次切換（height, round）。
// BroadcastViewChange 廣播本節點的 view-change 票（M12：含驗證人簽名，多數認證）。
func (n *Network) BroadcastViewChange(height int64, round int32, validator, signature string) {
	if height <= 0 || validator == "" || signature == "" {
		return
	}
	n.markSeenViewChange(viewChangeSeenKey(height, round, validator))
	n.flood(gossipReq{
		Kind: "viewchange", TTL: defaultTTL, From: n.cfg.SelfID,
		ViewChg: &ViewChangeMsg{
			Height: height, Round: round,
			Validator: validator, Signature: signature,
		},
	})
}

// receiveViewChange 處理網絡傳來的 view-change：去重 → 交節點 → 轉發。
func (n *Network) receiveViewChange(vc *ViewChangeMsg, ttl int, from string) {
	if vc == nil || vc.Height <= 0 || vc.Validator == "" || vc.Signature == "" {
		return
	}
	if !n.markSeenViewChange(viewChangeSeenKey(vc.Height, vc.Round, vc.Validator)) {
		return
	}
	if err := n.host.OnIncomingViewChange(vc.Height, vc.Round, vc.Validator, vc.Signature); err != nil {
		log.Printf("[p2p] 接收 view-change h=%d r=%d 失敗: %v",
			vc.Height, vc.Round, err)
	}
	if ttl > 0 {
		n.flood(gossipReq{
			Kind: "viewchange", TTL: ttl - 1, From: from, ViewChg: vc,
		})
	}
}

func (n *Network) markSeenViewChange(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seenViewCh[key]; ok {
		return false
	}
	n.seenViewCh[key] = struct{}{}
	return true
}

func viewChangeSeenKey(height int64, round int32, validator string) string {
	return fmt.Sprintf("%d:%d:%s", height, round, validator)
}

// BroadcastBridgeProposal 廣播跨鏈提案消息（含完整跨鏈指令）。
// 先做深拷貝快照：發起方後續會繼續在本地聚合簽名，廣播對象不得共享。
func (n *Network) BroadcastBridgeProposal(m *bridge.CrossChainMessage) {
	if m == nil || m.MessageID == "" {
		return
	}
	snap := cloneBridgeMessage(m)
	if snap == nil {
		return
	}
	n.markSeenBridge(bridgeSeenKey("propose", m.MessageID, ""))
	n.floodAll(gossipReq{
		Kind: "bridge", TTL: defaultTTL, From: n.cfg.SelfID,
		Bridge: &BridgeGossip{Type: "propose", MessageID: m.MessageID, Message: snap},
	})
	log.Printf("[bridge] 廣播跨鏈提案 %s（已知節點 %d）", m.MessageID, len(n.snapshotPeers()))
}

// BroadcastBridgeSig 廣播守衛對某提案的簽名。
func (n *Network) BroadcastBridgeSig(messageID string, sig *bridge.ValidatorSig, m *bridge.CrossChainMessage) {
	if messageID == "" || sig == nil || sig.Validator == "" {
		return
	}
	// 深拷貝快照，避免與消息對象的其他修改並行造成數據競爭。
	var msgSnap *bridge.CrossChainMessage
	if m != nil {
		msgSnap = cloneBridgeMessage(m)
		if msgSnap == nil {
			return
		}
	}
	n.markSeenBridge(bridgeSeenKey("sig", messageID, sig.Validator))
	n.floodAll(gossipReq{
		Kind: "bridge", TTL: defaultTTL, From: n.cfg.SelfID,
		Bridge: &BridgeGossip{Type: "sig", MessageID: messageID, Message: msgSnap, Sig: sig},
	})
}

// cloneBridgeMessage 深拷貝跨鏈消息（JSON 快照），用於廣播時與聚合對象隔離。
func cloneBridgeMessage(m *bridge.CrossChainMessage) *bridge.CrossChainMessage {
	if m == nil {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var out bridge.CrossChainMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return &out
}

// BroadcastBridgeExec 廣播跨鏈執行結果（提案方聚合達標後），供全網收斂狀態。
func (n *Network) BroadcastBridgeExec(bridgeTxID, targetTxHash, messageID string) {
	if bridgeTxID == "" {
		return
	}
	n.markSeenBridge(bridgeSeenKey("exec", messageID, bridgeTxID))
	n.floodAll(gossipReq{
		Kind: "bridge", TTL: defaultTTL, From: n.cfg.SelfID,
		Bridge: &BridgeGossip{
			Type: "exec", MessageID: messageID,
			BridgeTxID: bridgeTxID, TargetTxHash: targetTxHash,
		},
	})
}

// receiveBridge 處理網絡傳來的跨鏈消息：去重 → 交節點 → 遞減 TTL 轉發。
func (n *Network) receiveBridge(bg *BridgeGossip, ttl int, from string) {
	if bg == nil || bg.Type == "" {
		return
	}
	key := bridgeSeenKey(bg.Type, bg.MessageID, bridgeSigValidator(bg))
	if !n.markSeenBridge(key) {
		return
	}
	if err := n.host.OnIncomingBridge(bg); err != nil {
		log.Printf("[p2p] 接收跨鏈消息(%s)失敗: %v", bg.Type, err)
	}
	if ttl > 0 {
		n.flood(gossipReq{
			Kind: "bridge", TTL: ttl - 1, From: from, Bridge: bg,
		})
	}
}

func (n *Network) markSeenBridge(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.seenBridge[key]; ok {
		return false
	}
	n.seenBridge[key] = struct{}{}
	return true
}

func bridgeSigValidator(bg *BridgeGossip) string {
	if bg.Sig != nil {
		return bg.Sig.Validator
	}
	return bg.BridgeTxID
}

func bridgeSeenKey(typ, messageID, sub string) string {
	return typ + ":" + messageID + ":" + sub
}
