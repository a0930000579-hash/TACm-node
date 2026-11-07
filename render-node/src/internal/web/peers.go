package web

import (
	"fmt"
	"os"
	"strings"

	"tacm/internal/config"
)

// SeedInfo 為一個公開種子節點（M63：節點加入指引）。
// 新節點以 -p2p-seed 啟動時連這些節點完成握手並取得全網 peer 列表。
type SeedInfo struct {
	NodeID string `json:"node_id"`
	URL    string `json:"url"`
	Role   string `json:"role"` // 錨點 / 驗證節點
}

// mainnetSeeds 為主網 tacm-mainnet-1 的公開 seed 列表，由統一引導清單
// （config.BootstrapSeeds，可用 TACM_SEEDS 覆蓋）即時建立，所有節點與入口
// 看到的清單一致，且不再標示任何特殊「錨點」——各節點角色皆為對等驗證節點。
var mainnetSeeds = buildMainnetSeeds()

func buildMainnetSeeds() []SeedInfo {
	urls := config.BootstrapSeeds()
	out := make([]SeedInfo, 0, len(urls))
	for i, u := range urls {
		out = append(out, SeedInfo{
			NodeID: fmt.Sprintf("validator-%d", i+1),
			URL:    u,
			Role:   "驗證節點",
		})
	}
	return out
}

// serverSeeds 依網段回傳公開 seed 列表（M65）：
//   - mainnet：固定 mainnetSeeds（編譯進所有節點，全網一致）；
//   - testnet：由環境變量 TACM_TESTNET_SEEDS 配置（逗號分隔 URL），未配置則為空
//     （testnet 屬實驗網段，由起錨節點自行播報 seed，不與主網混淆）。
func (s *Server) serverSeeds() []SeedInfo {
	if s.network != "testnet" {
		return mainnetSeeds
	}
	raw := strings.TrimSpace(os.Getenv("TACM_TESTNET_SEEDS"))
	if raw == "" {
		return nil
	}
	var out []SeedInfo
	i := 0
	for _, u := range strings.Split(raw, ",") {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		i++
		out = append(out, SeedInfo{NodeID: fmt.Sprintf("testnet-%d", i), URL: u, Role: "testnet"})
	}
	return out
}
