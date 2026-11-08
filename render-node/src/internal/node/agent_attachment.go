package node

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 上傳文字類附件的副檔名（可直接讀取內容塞進模型上下文）。
// 圖片／影片為二進位，需視覺模型，不在此處讀取。
var textAttachmentExts = map[string]bool{
	".txt": true, ".md": true, ".json": true, ".csv": true, ".log": true,
	".go": true, ".js": true, ".ts": true, ".tsx": true, ".jsx": true,
	".py": true, ".java": true, ".rs": true, ".c": true, ".h": true,
	".cpp": true, ".sh": true, ".yaml": true, ".yml": true,
}

const (
	attachmentMaxPerFile = 200 << 10 // 單檔最多注入 200KB 文字
	attachmentMaxTotal   = 240 << 10
)

// 前端格式：[attachment: <原始檔名> <url，如 /uploads/xxxx.ext>]
var attachmentRefRE = regexp.MustCompile(`\[attachment:\s+(\S+)\s+(\S+)\]`)

// 上傳檔名規則：16 位元組隨機＝32 個十六進位字元＋副檔名；只接受此形態防路徑穿越。
var uploadNameRE = regexp.MustCompile(`^[0-9a-f]{32}\.[A-Za-z0-9]+$`)

// uploadsBaseDir 本機上傳目錄（預設相對 CWD 的 uploads；測試可覆蓋）。
var uploadsBaseDir = "uploads"

// resolveUploadPath 把 /uploads/<name> 對應到本機 uploads 目錄，並做嚴格校驗。
func resolveUploadPath(url string) (string, bool) {
	name := strings.TrimPrefix(url, "/uploads/")
	if name == url || !uploadNameRE.MatchString(name) {
		return "", false
	}
	return filepath.Join(uploadsBaseDir, name), true
}

// ingestAttachments 掃描訊息中的附件參考：文字類直接讀內容並內嵌，
// 圖片／影片保留參考並加註需視覺能力。回傳補充後的訊息。
func ingestAttachments(message string) string {
	refs := attachmentRefRE.FindAllStringSubmatch(message, -1)
	if len(refs) == 0 {
		return message
	}
	var b strings.Builder
	b.WriteString(message)
	total := 0
	for _, ref := range refs {
		origName, url := ref[1], ref[2]
		ext := strings.ToLower(filepath.Ext(url))
		path, ok := resolveUploadPath(url)
		if !ok {
			b.WriteString("\n[note: 附件路徑不合法：" + url + "]")
			continue
		}
		if !textAttachmentExts[ext] {
			b.WriteString("\n[note: 附件「" + origName + "」為圖片或影片（" + ext + "），需視覺模型才能解讀內容；目前僅提供連結 " + url + "]")
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			b.WriteString("\n[note: 無法讀取附件「" + origName + "」：" + err.Error() + "]")
			continue
		}
		content := string(data)
		if len(content) > attachmentMaxPerFile {
			content = content[:attachmentMaxPerFile] + "\n…（已截斷，僅含前 200KB）"
		}
		if total+len(content) > attachmentMaxTotal {
			b.WriteString("\n[note: 附件總量過大，跳過「" + origName + "」]")
			continue
		}
		total += len(content)
		b.WriteString("\n[attached file: " + origName + " " + url + "]\n" + content + "\n[end of " + origName + "]")
	}
	return b.String()
}
