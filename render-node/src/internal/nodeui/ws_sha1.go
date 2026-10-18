package nodeui

// wsSHA1Of：RFC6455 Sec-WebSocket-Accept 計算（SHA-1 + base64），
// 與主鏈 internal/web/ws.go 同構——nodeui 為獨立 binary 故在此重複最小實作。

import (
	"crypto/sha1"
	"encoding/base64"
)

func wsSHA1Of(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
