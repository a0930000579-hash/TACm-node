package crypto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// EncodeEthRawTx 把 EthTx（含已回填的 V/R/S）編碼為 EIP-155 類型 0 的 RLP raw。
func EncodeEthRawTx(tx *EthTx) []byte {
	if tx.V == nil {
		tx.V = big.NewInt(0)
	}
	if tx.R == nil {
		tx.R = big.NewInt(0)
	}
	if tx.S == nil {
		tx.S = big.NewInt(0)
	}
	return rlpEncodeList(
		rlpEncodeInt(tx.Nonce),
		rlpEncodeInt(tx.GasPrice),
		rlpEncodeInt(tx.Gas),
		rlpEncodeBytes(tx.To),
		rlpEncodeInt(tx.Value),
		rlpEncodeBytes(tx.Data),
		rlpEncodeInt(tx.V),
		rlpEncodeInt(tx.R),
		rlpEncodeInt(tx.S),
	)
}

// SignEthRawTx 以 EIP-155 簽署 type 0 交易：回填 V/R/S 並回完整 RLP raw。
// chainID>0 時 v = 35 + 2*chainID + recid；否則 v = 27 + recid。
func SignEthRawTx(tx *EthTx, priv *btcec.PrivateKey, chainID int64) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("eth: 簽署私鑰為空")
	}
	payload := tx.Payload()
	digest := Keccak256(payload)
	sig := ecdsa.Sign(priv, digest) // 與 RecoverEthSigner/VerifyEthTx 同路徑
	r, s, err := parseDERSig(sig.Serialize())
	if err != nil {
		return nil, err
	}
	// 找 recid：試 0..3 恢復比對（與 eth 相容層 VerifyEthTx 相同路徑）。
	var recid byte
	for i := byte(0); i < 4; i++ {
		var v *big.Int
		if chainID > 0 {
			v = big.NewInt(int64(35 + 2*chainID + int64(i)))
		} else {
			v = big.NewInt(int64(27 + i))
		}
		pub, err := RecoverEthSigner(payload, r, s, v, chainID)
		if err == nil && pub.IsEqual(priv.PubKey()) {
			recid = i
			break
		}
	}
	var v *big.Int
	if chainID > 0 {
		v = big.NewInt(int64(35 + 2*chainID + int64(recid)))
	} else {
		v = big.NewInt(int64(27 + recid))
	}
	tx.V, tx.R, tx.S = v, r, s
	return EncodeEthRawTx(tx), nil
}

// parseDERSig 解析 DER 簽名（30 len 02 len r 02 len s）回 r/s。
func parseDERSig(der []byte) (*big.Int, *big.Int, error) {
	if len(der) < 8 || der[0] != 0x30 {
		return nil, nil, errors.New("eth: 非法 DER 簽名")
	}
	// 跳過總長度（短格式）。
	pos := 2
	if der[1]&0x80 != 0 {
		pos += int(der[1] & 0x7F)
	}
	readInt := func() (*big.Int, int, error) {
		if pos+2 > len(der) || der[pos] != 0x02 {
			return nil, 0, errors.New("eth: DER 缺整數標記")
		}
		l := int(der[pos+1])
		if pos+2+l > len(der) {
			return nil, 0, errors.New("eth: DER 整數截斷")
		}
		n := new(big.Int).SetBytes(der[pos+2 : pos+2+l])
		return n, pos + 2 + l, nil
	}
	r, np, err := readInt()
	if err != nil {
		return nil, nil, err
	}
	pos = np
	s, _, err := readInt()
	if err != nil {
		return nil, nil, err
	}
	if r.Sign() <= 0 || s.Sign() <= 0 {
		return nil, nil, errors.New("eth: DER r/s 非正")
	}
	return r, s, nil
}

// ABISelector 回函數簽章的前 4 字節選擇器（keccak256(sig)[:4]）。
func ABISelector(sig string) [4]byte {
	h := Keccak256([]byte(sig))
	var out [4]byte
	copy(out[:], h[:4])
	return out
}

// ABIEncodeCall 把函數 selector 與參數編碼為 ABI calldata。
// 支援參數型別：uint256（*big.Int/int64/uint64/int）、address（[]byte 20B 或 0x 字串）、
// bytes32（[32]byte）、bool、string、[]byte（動態）。
func ABIEncodeCall(selector [4]byte, args ...any) ([]byte, error) {
	head := make([]byte, 0, len(args)*32)
	tail := make([]byte, 0, 64)
	for _, a := range args {
		switch v := a.(type) {
		case *big.Int:
			head = append(head, pad32(v)...)
		case big.Int:
			head = append(head, pad32(&v)...)
		case int64:
			head = append(head, pad32(big.NewInt(v))...)
		case uint64:
			head = append(head, pad32(new(big.Int).SetUint64(v))...)
		case int:
			head = append(head, pad32(big.NewInt(int64(v)))...)
		case uint32:
			head = append(head, pad32(new(big.Int).SetUint64(uint64(v)))...)
		case bool:
			if v {
				head = append(head, pad32(big.NewInt(1))...)
			} else {
				head = append(head, make([]byte, 32)...)
			}
		case [32]byte:
			head = append(head, v[:]...)
		case []byte:
			if len(v) == 20 {
				// 20 字節視為 address（靜態，ABI 左對齊）。
				head = append(head, append(make([]byte, 12), v...)...)
				continue
			}
			// 其餘 []byte 為動態 bytes：offset（head 已有長度）→ tail 長度+資料。
			off := big.NewInt(int64(len(head) + len(tail)))
			head = append(head, pad32(off)...)
			tail = append(tail, pad32(big.NewInt(int64(len(v))))...)
			tail = append(tail, v...)
			tail = padTail(tail)
		case string:
			off := big.NewInt(int64(len(head) + len(tail)))
			head = append(head, pad32(off)...)
			b := []byte(v)
			tail = append(tail, pad32(big.NewInt(int64(len(b))))...)
			tail = append(tail, b...)
			tail = padTail(tail)
		default:
			return nil, fmt.Errorf("eth: 不支援 ABI 參數型別 %T", a)
		}
	}
	out := make([]byte, 0, 4+len(head)+len(tail))
	out = append(out, selector[:]...)
	out = append(out, head...)
	out = append(out, tail...)
	return out, nil
}

func padTail(b []byte) []byte {
	rem := len(b) % 32
	if rem == 0 {
		return b
	}
	return append(b, make([]byte, 32-rem)...)
}

// ABIDecodeResult 解碼 ABI return 資料。types 支援 uint256/address/bytes32/bool/string/bytes。
func ABIDecodeResult(data []byte, types []string) ([]any, error) {
	out := make([]any, 0, len(types))
	pos := 0
	for _, typ := range types {
		t := strings.TrimSpace(typ)
		if strings.HasPrefix(t, "uint") {
			if pos+32 > len(data) {
				return nil, errors.New("eth: ABI 解碼截斷")
			}
			out = append(out, new(big.Int).SetBytes(data[pos:pos+32]))
			pos += 32
		} else if t == "address" {
			if pos+32 > len(data) {
				return nil, errors.New("eth: ABI 解碼截斷")
			}
			out = append(out, append([]byte(nil), data[pos+12:pos+32]...))
			pos += 32
		} else if t == "bytes32" {
			if pos+32 > len(data) {
				return nil, errors.New("eth: ABI 解碼截斷")
			}
			var b [32]byte
			copy(b[:], data[pos:pos+32])
			out = append(out, b)
			pos += 32
		} else if t == "bool" {
			if pos+32 > len(data) {
				return nil, errors.New("eth: ABI 解碼截斷")
			}
			out = append(out, new(big.Int).SetBytes(data[pos:pos+32]).Sign() != 0)
			pos += 32
		} else if t == "string" || t == "bytes" {
			if pos+32 > len(data) {
				return nil, errors.New("eth: ABI 解碼截斷")
			}
			off := new(big.Int).SetBytes(data[pos : pos+32]).Int64()
			pos += 32
			if off+32 > int64(len(data)) {
				return nil, errors.New("eth: ABI 動態偏移越界")
			}
			l := new(big.Int).SetBytes(data[off : off+32]).Int64()
			if off+32+l > int64(len(data)) {
				return nil, errors.New("eth: ABI 動態長度越界")
			}
			b := data[off+32 : off+32+l]
			if t == "string" {
				out = append(out, string(b))
			} else {
				out = append(out, b)
			}
		} else {
			return nil, fmt.Errorf("eth: 不支援 ABI 解碼型別 %q", typ)
		}
	}
	return out, nil
}

// pad32 把數值左對齊補到 32 字節（ABI uint 表示）。
func pad32(v *big.Int) []byte {
	out := make([]byte, 32)
	if v != nil && v.Sign() != 0 {
		b := v.Bytes()
		copy(out[32-len(b):], b)
	}
	return out
}

// EthAddressBytes 把 0x 或 40 hex 地址轉 20 字節。
func EthAddressBytes(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if len(s) != 40 {
		return nil, fmt.Errorf("eth: 地址應為 20 字節, got %d", len(s)/2)
	}
	b, err := hexDecode(s)
	if err != nil {
		return nil, fmt.Errorf("eth: 地址 hex 錯誤: %w", err)
	}
	return b, nil
}

// EthAddressHex 把 20 字節轉 0x 地址。
func EthAddressHex(b []byte) string {
	return "0x" + hexEncode(b)
}

// Uint256Bytes 把 *big.Int 轉 32 字節（ABI uint256）。
func Uint256Bytes(v *big.Int) []byte { return pad32(v) }

// Uint64Bytes 把 uint64 轉 32 字節。
func Uint64Bytes(v uint64) []byte {
	var b [32]byte
	binary.BigEndian.PutUint64(b[24:], v)
	return b[:]
}

// eth 相容 hex helpers（避免引入 encoding/hex 別名衝突用）。
// HexDecode 解碼 hex 字串（不帶 0x）。
func HexDecode(s string) ([]byte, error) {
	return hexDecode(s)
}

// HexEncode 編碼為小寫 hex。
func HexEncode(b []byte) string { return hexEncode(b) }

// EthKey 為乙太坊風 ECDSA 私鑰（btcec v2）。
type EthKey = btcec.PrivateKey

// EthKeyFromHex 從 32 字節 hex 私鑰建金鑰。
func EthKeyFromHex(hex string) (*EthKey, error) {
	b, err := hexDecode(strings.TrimPrefix(strings.TrimSpace(hex), "0x"))
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("eth: 私鑰應為 32 字節, got %d", len(b))
	}
	priv, _ := btcec.PrivKeyFromBytes(b)
	return priv, nil
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, errors.New("odd length hex")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi := hexVal(s[i*2])
		lo := hexVal(s[i*2+1])
		if hi < 0 || lo < 0 {
			return nil, errors.New("invalid hex char")
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out, nil
}

func hexEncode(b []byte) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexChars[c>>4]
		out[i*2+1] = hexChars[c&0x0f]
	}
	return string(out)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// EncodeEthRawTxType2 把 type 2 交易編碼為 EIP-1559 RLP raw（0x02 前綴）。
func EncodeEthRawTxType2(tx *EthTx) []byte {
	if tx.V == nil {
		tx.V = big.NewInt(0)
	}
	if tx.R == nil {
		tx.R = big.NewInt(0)
	}
	if tx.S == nil {
		tx.S = big.NewInt(0)
	}
	body := rlpEncodeList(
		rlpEncodeInt(tx.ChainID),
		rlpEncodeInt(tx.Nonce),
		rlpEncodeInt(tx.MaxPriorityFee),
		rlpEncodeInt(tx.MaxFee),
		rlpEncodeInt(tx.Gas),
		rlpEncodeBytes(tx.To),
		rlpEncodeInt(tx.Value),
		rlpEncodeBytes(tx.Data),
		rlpEncodeList(), // accessList（空）
		rlpEncodeInt(tx.V), // yParity
		rlpEncodeInt(tx.R),
		rlpEncodeInt(tx.S),
	)
	return append([]byte{0x02}, body...)
}

// SignEthRawTxType2 以 EIP-1559 簽署 type 2 交易：回填 yParity/R/S 並回完整 RLP raw。
func SignEthRawTxType2(tx *EthTx, priv *btcec.PrivateKey) ([]byte, error) {
	if priv == nil {
		return nil, errors.New("eth: 簽署私鑰為空")
	}
	if tx.TxType != 2 {
		tx.TxType = 2
	}
	payload := tx.Payload()
	digest := Keccak256(payload)
	sig := ecdsa.Sign(priv, digest)
	r, s, err := parseDERSig(sig.Serialize())
	if err != nil {
		return nil, err
	}
	var recid byte
	for i := byte(0); i < 4; i++ {
		pub, err := RecoverEthSignerType2(payload, r, s, big.NewInt(int64(i)))
		if err == nil && pub.IsEqual(priv.PubKey()) {
			recid = i
			break
		}
	}
	tx.V = big.NewInt(int64(recid))
	tx.R = r
	tx.S = s
	return EncodeEthRawTxType2(tx), nil
}
