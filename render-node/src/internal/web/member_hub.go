package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flosch/pongo2/v6"
)

// 與 internal/node/agent_billing.go 一致的 AI 計費識別。
const (
	aiHighDailyTacm  = 1.0 // 當日 AI 消耗達 1 TACm 即高用量提醒
	aiUsageScanLimit = 5000
)

// uploadFileInfo 為 uploads 目錄單一檔案。
type uploadFileInfo struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

// handleUploadList GET /api/uploads/list — 列出 uploads/ 全部檔案（新→舊）。
func (s *Server) handleUploadList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, map[string]any{"ok": true, "files": []uploadFileInfo{}})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	files := make([]uploadFileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		files = append(files, uploadFileInfo{
			Name:    name,
			URL:     "/uploads/" + name,
			Type:    mediaTypeForName(name),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		})
	}
	// 最新修改排最前。
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}
	writeJSON(w, map[string]any{"ok": true, "files": files})
}

func mediaTypeForName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return "image"
	case ".mp4", ".webm", ".mov":
		return "video"
	default:
		return "file"
	}
}

// usageWindow 為時段性消耗。
type usageWindow struct {
	Label string  `json:"label"`
	Spent float64 `json:"spent"`
	Count int     `json:"count"`
}

// memberUsage 為會員中心用量視圖。
type memberUsage struct {
	OK            bool          `json:"ok"`
	Address       string        `json:"address"`
	TotalSpent    float64       `json:"total_spent"`
	TotalCount    int           `json:"total_count"`
	HighUsage     bool          `json:"high_usage"`
	HighDailyTacm float64       `json:"high_daily_threshold"`
	Windows       []usageWindow `json:"windows"`
	Recent        []usageTx     `json:"recent"`
}

// usageTx 為單一 AI 計費流水。
type usageTx struct {
	Amount float64 `json:"amount"`
	Ts     int64   `json:"ts"`
	Memo   string  `json:"memo"`
}

// handleMemberUsage GET /api/member/usage?address=
// 口徑來自錢包雙分錄帳本（使用者本人 transfer_out、memo 前綴 titan:ai:），
// 而非鏈上交易索引——站內計費只入 wallet_ledger，不產生鏈上 from/to 交易。
func (s *Server) handleMemberUsage(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("address")
	if addr == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"ok": false, "error": "address_required"})
		return
	}
	items, err := s.ds.MemberLedger(addr, aiUsageScanLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	dayStart := startOfDay(now).Unix()
	weekStart := now.AddDate(0, 0, -7).Unix()
	monthStart := now.AddDate(0, 0, -30).Unix()

	var (
		winDay, winWeek, winMonth usageWindow
		total                     float64
		totalCount                int
		recent                    []usageTx
	)
	winDay.Label, winWeek.Label, winMonth.Label = "today", "last_7d", "last_30d"
	// MemberLedger 已新→舊。
	for _, it := range items {
		amt := it.Amount
		total += amt
		totalCount++
		if len(recent) < 12 {
			recent = append(recent, usageTx{Amount: amt, Ts: it.Ts, Memo: it.Memo})
		}
		if it.Ts >= monthStart {
			winMonth.Spent += amt
			winMonth.Count++
		}
		if it.Ts >= weekStart {
			winWeek.Spent += amt
			winWeek.Count++
		}
		if it.Ts >= dayStart {
			winDay.Spent += amt
			winDay.Count++
		}
	}
	writeJSON(w, memberUsage{
		OK:            true,
		Address:       addr,
		TotalSpent:    total,
		TotalCount:    totalCount,
		HighUsage:     winDay.Spent >= aiHighDailyTacm,
		HighDailyTacm: aiHighDailyTacm,
		Windows:       []usageWindow{winDay, winWeek, winMonth},
		Recent:        recent,
	})
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// 獨立頁面（與 /agent 同級，全螢幕 standalone 模板）。
func (s *Server) handleMemberPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "member.html", pongo2.Context{})
}

func (s *Server) handleFileLibrary(w http.ResponseWriter, r *http.Request) {
	s.render(w, "file-library.html", pongo2.Context{})
}

func (s *Server) handleMediaStudio(w http.ResponseWriter, r *http.Request) {
	s.render(w, "media-studio.html", pongo2.Context{})
}
