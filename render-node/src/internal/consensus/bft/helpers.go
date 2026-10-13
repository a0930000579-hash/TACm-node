package bft

import (
	"encoding/hex"
	"errors"
	"strings"
)

var errValidatorCount = errors.New("bft: 驗證人數與密鑰數不一致")

func rep(s string, n int) string { return strings.Repeat(s, n) }

func hexEncode(b []byte) string { return hex.EncodeToString(b) }
