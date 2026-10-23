package node

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"tacm/internal/chaindb"
)

// handleMetrics GET /metrics — Prometheus text/plain 指標端點（B3 監控告警數據源）。
// 指標覆蓋：出塊高度/最終高度/難度/交易池/手續費/運行時長/P2P 對等節點/磁碟/橋暫停，
// 供 Prometheus 抓取並由告警規則（deploy/alerts.yml）觸發。
func (s *RPCServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	st := s.node.GetStatus()
	var b strings.Builder
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	emit := func(name, help, v string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, v)
	}

	emit("tac_chain_height", "Current chain block height.", strconv.FormatInt(st.BlockHeight, 10))
	emit("tac_chain_finalized_height", "Finalized block height.", strconv.FormatInt(st.FinalHeight, 10))
	emit("tac_chain_difficulty", "Effective PoT difficulty.", strconv.Itoa(st.Difficulty))
	emit("tac_chain_mempool_size", "Pending transactions in mempool.", strconv.FormatInt(st.MempoolSize, 10))
	emit("tac_chain_base_fee", "Current base fee (TACm per gas).", strconv.FormatFloat(st.BaseFee, 'f', 8, 64))
	emit("tac_chain_gas_price", "Effective gas price (TACm per gas).", strconv.FormatFloat(st.GasPrice, 'f', 8, 64))
	emit("tac_node_uptime_seconds", "Node process uptime in seconds.", strconv.FormatInt(st.UptimeSec, 10))

	if peers, err := s.node.db.GetPeers(true); err == nil {
		emit("tac_node_peers", "Online P2P peers.", strconv.Itoa(len(peers)))
	}
	diskFree, diskTotal, diskOK := diskUsage(s.node.cfg.DataDir)
	if diskOK {
		emit("tac_node_disk_free_bytes", "Free bytes on data directory filesystem.", strconv.FormatUint(diskFree, 10))
		emit("tac_node_disk_total_bytes", "Total bytes on data directory filesystem.", strconv.FormatUint(diskTotal, 10))
	}
	if st.BridgeEnabled {
		paused := 0
		if s.node.bridge != nil && s.node.bridge.IsPaused() {
			paused = 1
		}
		emit("tac_bridge_paused", "Cross-chain bridge kill-switch state (1=paused).", strconv.Itoa(paused))
	}
	if _, err := w.Write([]byte(b.String())); err != nil {
		// 客戶端斷開時忽略寫入錯誤。
		_ = err
	}
}

// diskUsage 以 statfs 取得資料目錄所在檔案系統的可用/總位元組（Linux）。
// 失敗時回傳 ok=false（指標省略而非報錯，避免監控端點被檔案系統問題拖垮）。
func diskUsage(dir string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs, true
}

// peerCountForMetrics 供測試/其他端點復用：線上對等節點數。
func peerCountForMetrics(db *chaindb.ChainDB) int {
	peers, err := db.GetPeers(true)
	if err != nil {
		return 0
	}
	return len(peers)
}
