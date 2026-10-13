// Package p2p 實現 TAC Ai 智能鏈的多節點網絡層：節點握手與自動發現、
// 區塊/交易 gossip 廣播、落後節點主動同步與鏈重組。傳輸採用 HTTP/JSON，
// 以 goroutine、context 與顯式 error 實現，並發安全。
package p2p

import (
	"time"

	"tacm/internal/bridge"
	"tacm/internal/chaindb"
	"tacm/internal/consensus/bft"
)

// ProtocolVersion 為 P2P 協議版本，握手不一致則拒絕對接。
const ProtocolVersion = 1

// Peer 為一個已連接的對等節點。
type Peer struct {
	NodeID    string    `json:"node_id"`
	BaseURL   string    `json:"base_url"`
	Owner     string    `json:"owner"`
	Height    int64     `json:"height"`
	Finalized int64     `json:"finalized_height"`
	Online    bool      `json:"online"`
	LastSeen  time.Time `json:"last_seen"`
	failCount int
}

// Config 為 P2P 網絡配置。
type Config struct {
	SelfID            string
	SelfURL           string // 本節點對外可達的基址（如 http://127.0.0.1:8333）
	Owner             string
	Bootstrap         []string // 初始引導節點基址
	HeartbeatInterval time.Duration
	SyncInterval      time.Duration
	DialTimeout       time.Duration
	MaxPeers          int
}

// Host 抽象 P2P 所需的節點能力，由節點層實現（適配），避免網絡層與節點強耦合。
type Host interface {
	// Height 返回本節點當前鏈頂高度。
	Height() int64
	// FinalizedHeight 返回已最終化高度。
	FinalizedHeight() int64
	// OnIncomingBlock 驗證並嘗試接入一個網絡傳來的區塊（含其交易）。
	OnIncomingBlock(block *chaindb.Block, txs []chaindb.Transaction) error
	// OnIncomingTx 驗證並接收一個網絡傳來的交易（進入內存池/賬本）。
	OnIncomingTx(tx map[string]any) error
	// OnIncomingVote 驗證並接收一個網絡傳來的共識投票（prevote/precommit）。
	OnIncomingVote(vote *bft.Vote) error
	// OnIncomingViewChange 接收一個 view-change（M12：帶驗證人與簽名，多數認證後切換輪次）。
	OnIncomingViewChange(height int64, round int32, validator, signature string) error
	// OnIncomingBridge 接收一個跨鏈橋網絡消息（提案/守衛簽名/執行結果）。
	OnIncomingBridge(bg *BridgeGossip) error
	// BlockDetail 返回指定高度區塊及其交易（同步拉取用）；不存在返回 (nil,nil,nil)。
	BlockDetail(height int64) (*chaindb.Block, []chaindb.Transaction, error)
	// ReorgChain 在共同分叉點 forkPoint 之後切換到 blocks 給定的新鏈：
	// 截斷本地 forkPoint 之後數據並順序重放；節點僅在新鏈累積難度更高時接受。
	ReorgChain(forkPoint int64, blocks []chaindb.Block, txs [][]chaindb.Transaction) error
}

// BridgeGossip 為跨鏈橋網絡消息：提案（propose）/守衛簽名（sig）/執行結果（exec）。
type BridgeGossip struct {
	Type         string                    `json:"type"`
	MessageID    string                    `json:"message_id"`
	Message      *bridge.CrossChainMessage `json:"message,omitempty"`
	Sig          *bridge.ValidatorSig      `json:"sig,omitempty"`
	BridgeTxID   string                    `json:"bridge_tx_id,omitempty"`
	TargetTxHash string                    `json:"target_tx_hash,omitempty"`
}
