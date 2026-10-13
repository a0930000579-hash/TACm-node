package p2p

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"tacm/internal/chaindb"
	"tacm/internal/consensus/bft"
)

type helloReq struct {
	Version   int      `json:"version"`
	NodeID    string   `json:"node_id"`
	BaseURL   string   `json:"base_url"`
	RPCURL    string   `json:"rpc_url"` // 兼容 Python 字段名
	Owner     string   `json:"owner"`
	Height    int64    `json:"height"`
	Finalized int64    `json:"finalized_height"`
	Peers     []string `json:"peers"`
}

type helloResp struct {
	OK        bool     `json:"ok"`
	Version   int      `json:"version"`
	NodeID    string   `json:"node_id"`
	BaseURL   string   `json:"base_url"`
	Owner     string   `json:"owner"`
	Height    int64    `json:"height"`
	Finalized int64    `json:"finalized_height"`
	Peers     []string `json:"peers"`
}

func (n *Network) handleHello(w http.ResponseWriter, r *http.Request) {
	var req helloReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeP2PErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Version != ProtocolVersion {
		writeP2PErr(w, http.StatusBadRequest, "protocol version mismatch")
		return
	}
	url := req.BaseURL
	if url == "" {
		url = req.RPCURL
	}
	url = normURL(url)
	if url == "" || url == normURL(n.cfg.SelfURL) || req.NodeID == n.cfg.SelfID {
		writeP2PErr(w, http.StatusBadRequest, "cannot peer with self")
		return
	}

	n.registerPeer(req.NodeID, url, req.Owner, req.Height, req.Finalized)
	for _, p := range req.Peers {
		n.addPeerByURL(p)
	}
	writeP2PJSON(w, http.StatusOK, helloResp{
		OK: true, Version: ProtocolVersion, NodeID: n.cfg.SelfID,
		BaseURL: n.cfg.SelfURL, Owner: n.cfg.Owner,
		Height: n.host.Height(), Finalized: n.host.FinalizedHeight(),
		Peers: n.peerURLs(),
	})
}

type gossipReq struct {
	Kind     string                `json:"kind"`
	TTL      int                   `json:"ttl"`
	From     string                `json:"from"`
	Block    *chaindb.Block        `json:"block"`
	BlockTxs []chaindb.Transaction `json:"block_txs"`
	Tx       map[string]any        `json:"tx"`
	Txs      []map[string]any      `json:"txs"`
	Vote     *bft.Vote             `json:"vote"`
	ViewChg  *ViewChangeMsg        `json:"view_change"`
	Bridge   *BridgeGossip         `json:"bridge"`
}

// ViewChangeMsg 為提議超時後的輪次切換消息（M12：帶驗證人簽名，多數認證才生效）。
type ViewChangeMsg struct {
	Height    int64  `json:"height"`
	Round     int32  `json:"round"`
	Validator string `json:"validator"`
	Signature string `json:"signature"`
}

func (n *Network) handleGossip(w http.ResponseWriter, r *http.Request) {
	var req gossipReq
	dec := json.NewDecoder(r.Body)
	dec.UseNumber() // 保留數字字面量，避免 tx 的 ts/nonce 變 float64/科學計數破壞簽名重算
	if err := dec.Decode(&req); err != nil {
		writeP2PErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Kind == "bridge" {
		log.Printf("[p2p] 收到跨鏈消息 type=%s mid=%s from=%s", req.Bridge.Type, req.Bridge.MessageID, req.From)
	}
	switch req.Kind {
	case "block":
		if req.Block == nil {
			writeP2PErr(w, http.StatusBadRequest, "empty block")
			return
		}
		n.receiveBlock(req.Block, req.BlockTxs, req.TTL, req.From)
	case "tx":
		if req.Tx == nil {
			writeP2PErr(w, http.StatusBadRequest, "empty tx")
			return
		}
		n.receiveTx(req.Tx, req.TTL, req.From)
	case "txs":
		if len(req.Txs) == 0 {
			writeP2PErr(w, http.StatusBadRequest, "empty txs")
			return
		}
		n.receiveTxs(req.Txs, req.TTL, req.From)
	case "consensus":
		if req.Vote == nil {
			writeP2PErr(w, http.StatusBadRequest, "empty vote")
			return
		}
		n.receiveVote(req.Vote, req.TTL, req.From)
	case "viewchange":
		if req.ViewChg == nil {
			writeP2PErr(w, http.StatusBadRequest, "empty view change")
			return
		}
		n.receiveViewChange(req.ViewChg, req.TTL, req.From)
	case "bridge":
		if req.Bridge == nil {
			writeP2PErr(w, http.StatusBadRequest, "empty bridge message")
			return
		}
		n.receiveBridge(req.Bridge, req.TTL, req.From)
	default:
		writeP2PErr(w, http.StatusBadRequest, "unknown kind")
		return
	}
	writeP2PJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type blockData struct {
	Block *chaindb.Block        `json:"block"`
	Txs   []chaindb.Transaction `json:"txs"`
}

func (n *Network) handleBlockFetch(w http.ResponseWriter, r *http.Request) {
	h, err := strconv.ParseInt(r.PathValue("height"), 10, 64)
	if err != nil {
		writeP2PErr(w, http.StatusBadRequest, "invalid height")
		return
	}
	b, txs, err := n.host.BlockDetail(h)
	if err != nil {
		writeP2PErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b == nil {
		writeP2PErr(w, http.StatusNotFound, "block not found")
		return
	}
	writeP2PJSON(w, http.StatusOK, blockData{Block: b, Txs: txs})
}

// handleBlocksFetch M86：批量回傳 [from,to] 區塊（每塊含 txs），上限 500 塊——
// follower 晚加入/大缺口時一次拉取，取代逐塊 HTTP round-trip。
func (n *Network) handleBlocksFetch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := strconv.ParseInt(q.Get("from"), 10, 64)
	if err != nil {
		writeP2PErr(w, http.StatusBadRequest, "invalid from")
		return
	}
	to := from + 499
	if q.Get("to") != "" {
		to, err = strconv.ParseInt(q.Get("to"), 10, 64)
		if err != nil {
			writeP2PErr(w, http.StatusBadRequest, "invalid to")
			return
		}
	}
	if to < from || to-from > 499 {
		to = from + 499
	}
	out := make([]blockData, 0, to-from+1)
	for h := from; h <= to; h++ {
		b, txs, err := n.host.BlockDetail(h)
		if err != nil {
			writeP2PErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if b == nil {
			break // 鏈頂已到
		}
		out = append(out, blockData{Block: b, Txs: txs})
	}
	writeP2PJSON(w, http.StatusOK, map[string]any{"blocks": out})
}

func (n *Network) handlePeers(w http.ResponseWriter, r *http.Request) {
	writeP2PJSON(w, http.StatusOK, map[string]any{
		"peers": n.snapshotPeers(), "count": len(n.snapshotPeers()),
	})
}

func writeP2PJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeP2PErr(w http.ResponseWriter, status int, msg string) {
	writeP2PJSON(w, status, map[string]string{"error": msg})
}
