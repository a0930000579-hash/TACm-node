package config

import (
	"os"
	"strings"
)

// defaultBootstrapSeeds 為主網公開引導節點的內建清單（不含任何單一擁有者特權）。
// 新節點可向其中任一節點連線/抓快照，任一來源離線會自動輪詢下一個。
// 清單可由環境變數 TACM_SEEDS 覆蓋（逗號分隔），無需重新編譯即可更新引導來源。
var defaultBootstrapSeeds = []string{
	"http://2.28.201.174:8080",
	"https://tacm-ai-cyprto.onrender.com",
	"https://oyster-app-aynnb.ondigitalocean.app",
}

// BootstrapSeeds 回傳實際引導節點清單：TACM_SEEDS 有設定時以其為準，
// 否則使用內建 defaultBootstrapSeeds。會去除空白與結尾斜線、過濾空項目。
func BootstrapSeeds() []string {
	raw := strings.TrimSpace(os.Getenv("TACM_SEEDS"))
	src := defaultBootstrapSeeds
	if raw != "" {
		src = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(src))
	for _, s := range src {
		s = strings.TrimSpace(strings.TrimRight(s, "/"))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// JoinSeeds 把節點清單組成逗號分隔字串（供 flag 預設值使用）。
func JoinSeeds(seeds []string) string {
	return strings.Join(seeds, ",")
}

// PrimarySeed 回傳第一個引導節點（供單一入口按鈕/狀態顯示使用）。
func PrimarySeed() string {
	seeds := BootstrapSeeds()
	if len(seeds) == 0 {
		return ""
	}
	return seeds[0]
}
