package p2p

import (
	"net"
	"net/url"
	"strings"
	"time"
)

// stalePeerWindow：曾握手但長期無回應的節點，超過此窗口視為離線節點。
const stalePeerWindow = 30 * time.Minute

// hostFromURL 取出 BaseURL 的 host（不含 port）；解析失敗回空字串。
func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// 無 scheme 的 host:port 形態，補前綴再解析。
		if u2, e2 := url.Parse("http://" + raw); e2 == nil {
			return u2.Hostname()
		}
		return ""
	}
	return u.Hostname()
}

// isLoopbackOrPrivateHost 判斷主機是否為迴環或私有/鏈路本地位址（不可跨機路由）。
func isLoopbackOrPrivateHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false // 公開網域名稱，視為可達。
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// isRealPeer 判斷節點是否為應顯示的真實驗證節點：
//   - 在線或曾產出區塊（height>0）即保留；
//   - 從未握手（height0、無 LastSeen）且位址為迴環/私有 IP 的臨時條目，視為假節點過濾；
//   - 曾握手但長期離線（超過 stalePeerWindow）的條目亦過濾，避免污染面板。
func isRealPeer(p Peer) bool {
	handshaked := !p.LastSeen.IsZero()
	if p.Online || p.Height > 0 || p.Finalized > 0 {
		return true
	}
	if !handshaked {
		// 從未連上：僅過濾迴環/私有位址，公開網址保留（可能暫時離線）。
		host := hostFromURL(p.BaseURL)
		if host == "" {
			return false
		}
		return !isLoopbackOrPrivateHost(host)
	}
	// 曾握手但已離線：僅在長期無回應時隱藏。
	return time.Since(p.LastSeen) < stalePeerWindow
}

// DisplayPeers 過濾出真實驗證節點的快照，僅供面板/公開端點使用；
// 共識、同步與廣播邏輯仍使用完整的 PeerSnapshot。
func (n *Network) DisplayPeers() []Peer {
	all := n.snapshotPeers()
	out := make([]Peer, 0, len(all))
	for _, p := range all {
		if isRealPeer(p) {
			out = append(out, p)
		}
	}
	return out
}

// filterDisplayPeers 提供給同包外部使用的純函式版本。
func filterDisplayPeers(ps []Peer) []Peer {
	out := make([]Peer, 0, len(ps))
	for _, p := range ps {
		if isRealPeer(p) {
			out = append(out, p)
		}
	}
	return out
}

// addDiscoveredPeerURL 加入自其他節點 hello 回應中發現的位址；
// 拒絕迴環/私有/鏈路本地位址，避免誤設定節點回報的內網 IP 污染全網清單。
// 顯式 Bootstrap 與 admin_addPeer 仍走 addPeerByURL，本機開發不受影響。
func (n *Network) addDiscoveredPeerURL(rawURL string) {
	u := normURL(rawURL)
	if u == "" {
		return
	}
	if isLoopbackOrPrivateHost(hostFromURL(u)) {
		return
	}
	n.addPeerByURL(u)
}
