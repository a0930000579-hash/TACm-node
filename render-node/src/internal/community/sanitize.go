package community

import (
	"regexp"
	"strings"
)

// 富文本白名單 sanitizer：貼文內容允許的標籤與屬性。
// 目的：社群貼文支援粗體/斜體/標題/連結等富文本，同時阻斷 <script>、事件屬性與
// javascript: 連結，避免 XSS（OPSEC：任何存進鏈外資料庫並 render 的 HTML 都必須過此閘）。

var (
	reScript     = regexp.MustCompile(`(?i)<\s*/?\s*script[^>]*>`)
	reStyle      = regexp.MustCompile(`(?i)<\s*/?\s*style[^>]*>`)
	reEventAttr  = regexp.MustCompile(`(?i)\son\w+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reJSURI      = regexp.MustCompile(`(?i)\s(href|src)\s*=\s*("[^"]*javascript:[^"]*"|'[^']*javascript:[^']*'|\S*javascript:\S*)`)
	reStyleAttr  = regexp.MustCompile(`(?i)\sstyle\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reAllowedTag = regexp.MustCompile(`(?i)^(b|strong|i|em|u|s|p|br|h2|h3|a|ul|ol|li|blockquote|code|pre|span|div)$`)
)

// sanitizeRichText 清洗富文本：白名單標籤、剝離事件/樣式屬性、攔截 javascript: 連結。
func sanitizeRichText(in string) string {
	if in == "" {
		return ""
	}
	in = reScript.ReplaceAllString(in, "")
	in = reStyle.ReplaceAllString(in, "")
	in = reEventAttr.ReplaceAllString(in, "")
	in = reJSURI.ReplaceAllString(in, "")
	in = reStyleAttr.ReplaceAllString(in, "")

	// 標籤白名單過濾：保留開/閉標籤（含屬性僅 href），其餘標籤名稱改為純文字。
	var b strings.Builder
	for {
		idx := strings.IndexByte(in, '<')
		if idx < 0 {
			b.WriteString(in)
			break
		}
		b.WriteString(in[:idx])
		rest := in[idx:]
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			b.WriteString(rest)
			break
		}
		inner := rest[1:end]
		closeTag := strings.HasPrefix(inner, "/")
		name := strings.TrimSpace(strings.TrimPrefix(inner, "/"))
		if sp := strings.IndexAny(name, " \t\n"); sp >= 0 {
			name = name[:sp]
		}
		name = strings.TrimSpace(name)
		if reAllowedTag.MatchString(name) {
			if closeTag {
				b.WriteString("</" + strings.ToLower(name) + ">")
			} else {
				// 僅 <a> 保留 href（且已擋 javascript:）。
				attr := ""
				if strings.EqualFold(name, "a") {
					if m := regexp.MustCompile(`(?i)\shref\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`).FindString(inner); m != "" {
						attr = " " + strings.TrimSpace(m)
					}
				}
				b.WriteString("<" + strings.ToLower(name) + attr + ">")
			}
		} else {
			// 非白名單標籤 → 保留文字內容（剝標籤）。
			b.WriteString("&lt;" + inner + "&gt;")
		}
		in = rest[end+1:]
	}
	return b.String()
}
