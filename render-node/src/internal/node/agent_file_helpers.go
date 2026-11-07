package node

// agent_file_helpers.go — 檔名清洗、副檔名補齊與 MIME 對照（純函式，無外部狀態）。

import (
	"path"
	"strings"
)

// langExt 語言/格式 → 副檔名。
var langExt = map[string]string{
	"go": ".go", "golang": ".go",
	"python": ".py", "py": ".py",
	"javascript": ".js", "js": ".js", "node": ".js",
	"typescript": ".ts", "ts": ".ts",
	"html": ".html", "css": ".css",
	"json": ".json", "markdown": ".md", "md": ".md",
	"text": ".txt", "txt": ".txt",
	"sql": ".sql", "sh": ".sh", "bash": ".sh", "yaml": ".yaml", "yml": ".yaml",
}

// extMime 副檔名 → MIME。
var extMime = map[string]string{
	".go": "text/x-go; charset=utf-8", ".py": "text/x-python; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".ts": "text/typescript; charset=utf-8",
	".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
	".json": "application/json; charset=utf-8", ".md": "text/markdown; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".sql": "application/sql; charset=utf-8",
	".sh": "application/x-sh; charset=utf-8", ".yaml": "application/yaml; charset=utf-8",
}

// sanitizeFilename 移除路徑分隔與危險片段，只保留單純檔名。
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// 只取 base，去除任何目錄成分。
	name = path.Base(name)
	name = strings.ReplaceAll(name, "..", "")
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(name)
}

// ensureExtension 若檔名無副檔名，依語言補上；未知語言預設 .txt。
func ensureExtension(filename, language string) string {
	if ext := path.Ext(filename); ext != "" {
		return filename
	}
	if e, ok := langExt[language]; ok {
		return filename + e
	}
	return filename + ".txt"
}

// mimeForFile 依副檔名/語言回 MIME 與標準化語言標記；未知回 text/plain。
func mimeForFile(filename, language string) (string, string) {
	ext := strings.ToLower(path.Ext(filename))
	if mime, ok := extMime[ext]; ok {
		lang := language
		if lang == "" {
			lang = strings.TrimPrefix(ext, ".")
		}
		return mime, lang
	}
	return "text/plain; charset=utf-8", "text"
}
