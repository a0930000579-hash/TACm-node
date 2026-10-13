// Package nodeui 提供節點獨立品牌前端（M86）：節點運維者專用儀表板——
// 鏈狀態 / 同步進度 / P2P peers / 驗證人集 / 出塊記錄 / 節點金鑰與獎勵。
// 與主網 tacweb（landing/錢包/交易所等用戶功能）完全分離：獨立 binary
// (cmd/nodeweb)、獨立資料夾 (internal/nodeui)、獨立品牌「TAC Ai Smart Chain — Node」。
package nodeui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"tacm/internal/node"
	"tacm/internal/p2p"
)

//go:embed templates static
var assets embed.FS

// Server 為節點品牌 UI 的 HTTP 服務器；直接持 *node.Node（本機視角），
// 不走主網 DataSource 介面——節點儀表讀取的是節點自身鏈/網/共識狀態。
type Server struct {
	n          *node.Node
	mainnetURL string
	started    time.Time
	mux        *http.ServeMux
}

// New 建立節點 UI 服務器。mainnetURL 為開放挖礦/主網入口（預設錨點）。
func New(n *node.Node, mainnetURL string) *Server {
	if mainnetURL == "" {
		mainnetURL = "http://2.28.201.174:8080"
	}
	s := &Server{n: n, mainnetURL: strings.TrimRight(mainnetURL, "/"), started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/peers", s.handlePeers)
	mux.HandleFunc("GET /api/validators", s.handleValidators)
	mux.HandleFunc("GET /api/blocks", s.handleBlocks)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(mustSub(assets, "static")))))
	s.mux = mux
	return s
}

// Handler 回傳可掛載/監聽的 HTTP Handler。
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe 在指定 addr 上提供節點 UI。
func (s *Server) ListenAndServe(addr string) error {
	log.Printf("[nodeui] TAC Ai Smart Chain — Node UI listening on %s", addr)
	return http.ListenAndServe(addr, s.mux)
}

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

// handleIndex 回傳節點品牌首頁。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	raw, err := assets.ReadFile("templates/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 永不緩存 HTML：節點 UI 每次更新後手機直接取最新版（沿用 M84 原則）。
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	_, _ = w.Write(raw)
}

type statusPayload struct {
	node.Status
	NodeID       string `json:"node_id"`
	Address      string `json:"address"`
	IdentityKey  string `json:"identity_pub_hex"`
	Synced       bool   `json:"synced"`
	PeersTotal   int    `json:"peers_total"`
	BestPeerH    int64  `json:"best_peer_height"`
	BehindBlocks int64  `json:"behind_blocks"`
	Uptime       string `json:"uptime"`
	MainnetURL   string `json:"mainnet_url"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := s.n.GetStatus()
	bestH := st.BlockHeight
	online := 0
	if pn := s.n.P2PNet(); pn != nil {
		for _, p := range pn.PeerSnapshot() {
			if p.Online && p.Height > bestH {
				bestH = p.Height
			}
			if p.Online {
				online++
			}
		}
	}
	behind := bestH - st.BlockHeight
	if behind < 0 {
		behind = 0
	}
	writeJSON(w, statusPayload{
		Status:       st,
		NodeID:       st.NodeID,
		Address:      s.n.Address(),
		IdentityKey:  s.n.IdentityPubHex(),
		Synced:       behind == 0,
		PeersTotal:   online,
		BestPeerH:    bestH,
		BehindBlocks: behind,
		Uptime:       time.Since(s.started).Round(time.Second).String(),
		MainnetURL:   s.mainnetURL,
	})
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	var peers []p2p.Peer
	if pn := s.n.P2PNet(); pn != nil {
		peers = pn.PeerSnapshot()
	}
	writeJSON(w, map[string]any{"peers": peers})
}

func (s *Server) handleValidators(w http.ResponseWriter, r *http.Request) {
	vs := s.n.ValidatorSet()
	resp := map[string]any{
		"validators": vs.List(),
		"total":      vs.Len(),
		"power":      vs.TotalPower(),
		"quorum":     vs.QuorumPower(),
		"fault":      vs.FaultTolerance(),
		"dynamic":    false,
	}
	// M87：PoS 動態驗證人集（質押驅動，jailed 標記）。
	if ps := s.n.GetPosStore(); ps != nil {
		if vals, err := ps.GetActiveValidatorsForConsensus(21); err == nil && len(vals) > 0 {
			dyn := make([]map[string]any, 0, len(vals))
			for i := range vals {
				v := &vals[i]
				dyn = append(dyn, map[string]any{
					"address": v.Address, "name": v.Name, "stake": v.TotalStake,
					"power":  int(v.TotalStake/100) + 1,
					"status": v.Status, "jailed": v.Status == "jailed",
					"uptime": v.Uptime, "proposed": v.BlocksProposed, "missed": v.BlocksMissed,
				})
			}
			resp["dynamic"] = true
			resp["validators"] = dyn
			resp["total"] = len(dyn)
		}
	}
	writeJSON(w, resp)
}

// handleBlocks 回傳最近 N 塊（含 coinbase 獎勵結構摘要）。
func (s *Server) handleBlocks(w http.ResponseWriter, r *http.Request) {
	count := 10
	if v := r.URL.Query().Get("n"); v != "" {
		if c, err := strconv.Atoi(v); err == nil && c > 0 && c <= 30 {
			count = c
		}
	}
	tip := s.n.GetStatus().BlockHeight
	out := make([]map[string]any, 0, count)
	for h := tip; h >= 0 && len(out) < count; h-- {
		d, err := s.n.GetBlockDetail(h)
		if err != nil || d == nil {
			break
		}
		b := d.Block
		out = append(out, map[string]any{
			"height":           b.Height,
			"hash":             b.Hash,
			"proposer":         b.Proposer,
			"proposer_address": b.ProposerAddress,
			"ts":               b.Ts,
			"tx_count":         b.TxCount,
			"difficulty":       b.Difficulty,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["height"].(int64) < out[j]["height"].(int64) })
	writeJSON(w, map[string]any{"blocks": out, "tip": tip})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[nodeui] encode: %v", err)
	}
}
