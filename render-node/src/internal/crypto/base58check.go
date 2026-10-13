package crypto

import (
	"bytes"
	"errors"

	"github.com/btcsuite/btcd/btcutil/base58"
)

// 地址 / WIF 版本字節與壓縮公鑰標記（對照 Python tac_crypto 常量）。
const (
	AddrVersionByte   byte = 0x00 // 地址版本字節（P2PKH 式）
	WIFVersionByte    byte = 0x80 // WIF 私鑰版本字節
	CompressedFlag    byte = 0x01 // 壓縮公鑰標記
	hash160Len             = 20
	addressPayloadLen      = 1 + hash160Len // 版本字節 + 20 字節哈希 = 21
)

// ErrChecksum 表示 Base58Check 校驗和不匹配。
var ErrChecksum = errors.New("crypto: Base58Check 校驗和不匹配")

// base58CheckEncode 對 payload 追加 4 字節校驗和後做 Base58 編碼。
func base58CheckEncode(payload []byte) string {
	checksum := DoubleSHA256(payload)[:4]
	combined := make([]byte, 0, len(payload)+4)
	combined = append(combined, payload...)
	combined = append(combined, checksum...)
	return base58.Encode(combined)
}

// base58CheckDecode 做 Base58 解碼並驗證校驗和，返回去除校驗和的 payload。
func base58CheckDecode(s string) ([]byte, error) {
	raw := base58.Decode(s)
	if len(raw) < 4 {
		// base58.Decode 對非法輸入返回空切片；統一報校驗/格式錯誤。
		return nil, ErrChecksum
	}
	payload, checksum := raw[:len(raw)-4], raw[len(raw)-4:]
	expected := DoubleSHA256(payload)[:4]
	if !bytes.Equal(checksum, expected) {
		return nil, ErrChecksum
	}
	return payload, nil
}
