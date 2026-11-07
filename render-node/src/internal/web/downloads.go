package web

// downloads.go — v69：官方下載中心頁面（/downloads）。
// 取代 FileServer 的裸目錄列表：美化的版本下載中心（檔名/大小/更新時間/下載連結），
// 檔名與英文為主、附中文說明，供架設者直接下載節點程式與安裝腳本。

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"sort"
	"strings"
)

// downloadsIndex 渲染下載中心索引頁（目錄請求時）。
func (s *Server) downloadsIndex(w http.ResponseWriter, r *http.Request) {
	dir := downloadsDirPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, "downloads: 讀取目錄失敗", http.StatusInternalServerError)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})
	var rows strings.Builder
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		name := e.Name()
		rows.WriteString(fmt.Sprintf("<tr><td><a href=\"%s\" download>%s</a></td><td>%s</td><td>%s</td></tr>",
			html.EscapeString(name), html.EscapeString(name),
			formatBytes(info.Size()), info.ModTime().UTC().Format("2006-01-02 15:04")))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>TACm Downloads</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;background:#0f1220;color:#e6e9f5;margin:0;padding:40px 20px}
.wrap{max-width:860px;margin:0 auto}
h1{font-size:24px;margin:0 0 4px}
.sub{color:#8b93b5;font-size:14px;margin:0 0 28px}
table{width:100%%;border-collapse:collapse;background:#181c30;border-radius:12px;overflow:hidden}
th,td{text-align:left;padding:12px 16px;border-bottom:1px solid #262b45;font-size:14px}
th{color:#8b93b5;font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:.5px}
td a{color:#7aa2ff;text-decoration:none;font-weight:600}
td a:hover{text-decoration:underline}
.muted{color:#8b93b5}
.foot{margin-top:24px;color:#5d6485;font-size:12px}
</style></head><body><div class="wrap">
<h1>TACm Downloads</h1>
<p class="sub">Official node binaries, release packages and install script — hosted by the genesis node.</p>
<table><thead><tr><th>File</th><th>Size</th><th>Updated (UTC)</th></tr></thead><tbody>
%s
</tbody></table>
<p class="foot">One-line install (架設者一鍵安裝節點): bash &lt;(curl -fsSL <a href="install-node.sh">install-node.sh</a>)</p>
</div></body></html>`, rows.String())
}

// downloadsHandler /downloads/* 分派：目錄請求→美化索引；檔案請求→檔案服務（保持原下載行為）。
func (s *Server) downloadsHandler(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel == "" || strings.HasSuffix(rel, "/") {
		s.downloadsIndex(w, r)
		return
	}
	http.StripPrefix("/downloads/", http.FileServer(http.Dir(downloadsDirPath()))).ServeHTTP(w, r)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
