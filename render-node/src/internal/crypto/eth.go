package crypto

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"golang.org/x/crypto/sha3"
)

// ethCompat 提供 Ethereum 生態相容層：eth_* JSON-RPC 所需的最小 RLP
// 交易解碼與 EIP-155 簽名驗證。TAC 交易以 pubkey="eth:<payload>:<pub>"、
// signature="eth:<r||s||v>" 承載 eth 已驗證簽名（無 DB 遷移）。

// EthChainID 為 TAC 對外呈現的 EVM chainId（與 VM NewContext 一致）。
const EthChainID = 1337

// Keccak256 對多段 bytes 做 legacy keccak-256（以太坊哈希）。
func Keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// EthTx 為解碼後的 EIP-155 類型 0 交易。
type EthTx struct {
	Nonce    *big.Int
	GasPrice *big.Int // type 0 legacy gas price
	Gas      *big.Int
	To       []byte // 20 字節；空 = 合約建立
	Value    *big.Int
	Data     []byte
	V        *big.Int // type 0: EIP-155 v；type 2: yParity(0/1)
	R        *big.Int
	S        *big.Int
	Raw      []byte // 原始 RLP bytes
	// EIP-1559 type 2（M76 錢包整合：MetaMask 默認交易類型）。
	TxType         uint8  // 0 = legacy；2 = EIP-1559
	ChainID        *big.Int
	MaxPriorityFee *big.Int
	MaxFee         *big.Int
}

// Payload 返回簽名哈希輸入（不含 v/r/s）。
// type 0：RLP 前 6 欄；type 2：0x02 || RLP 前 9 欄（含 chainId/accessList）。
func (t *EthTx) Payload() []byte {
	if t.TxType == 2 {
		body := rlpEncodeList(
			rlpEncodeInt(t.ChainID),
			rlpEncodeInt(t.Nonce),
			rlpEncodeInt(t.MaxPriorityFee),
			rlpEncodeInt(t.MaxFee),
			rlpEncodeInt(t.Gas),
			rlpEncodeBytes(t.To),
			rlpEncodeInt(t.Value),
			rlpEncodeBytes(t.Data),
			rlpEncodeList(), // accessList（空）
		)
		return append([]byte{0x02}, body...)
	}
	return rlpEncodeList(
		rlpEncodeInt(t.Nonce),
		rlpEncodeInt(t.GasPrice),
		rlpEncodeInt(t.Gas),
		rlpEncodeBytes(t.To),
		rlpEncodeInt(t.Value),
		rlpEncodeBytes(t.Data),
	)
}

// ---- 簡易 RLP（僅覆蓋 eth 交易所需：bytes/int 與單層 list） ----

func rlpEncodeInt(v *big.Int) []byte {
	if v == nil || v.Sign() == 0 {
		return []byte{0x80}
	}
	b := v.Bytes() // 大端
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	return append([]byte{0x80 + byte(len(b))}, b...)
}

func rlpEncodeBytes(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	if len(b) < 56 {
		return append([]byte{0x80 + byte(len(b))}, b...)
	}
	lenB := big.NewInt(int64(len(b))).Bytes()
	return append(append([]byte{0xB7 + byte(len(lenB))}, lenB...), b...)
}

func rlpEncodeList(items ...[]byte) []byte {
	body := []byte{}
	for _, it := range items {
		body = append(body, it...)
	}
	if len(body) < 56 {
		return append([]byte{0xC0 + byte(len(body))}, body...)
	}
	lenB := big.NewInt(int64(len(body))).Bytes()
	return append(append([]byte{0xF7 + byte(len(lenB))}, lenB...), body...)
}

// rlpItem 為解碼結果：bytes（含 int）或 list。
type rlpItem struct {
	raw  []byte // 此 item 的完整 RLP 編碼
	data []byte // bytes/int 的內容；list 時為 nil
	list []rlpItem
}

// rlpDecodeItem 解碼單個 RLP item（支援單層 list，供 eth 交易用）。
func rlpDecodeItem(b []byte) (rlpItem, error) {
	if len(b) == 0 {
		return rlpItem{}, errors.New("rlp: 空輸入")
	}
	lead := b[0]
	body := b
	// 依 lead 解析內容與總長度（bytes 與 list 的長度規則不同）。
	shortLen := func(base byte) (int, error) {
		l := int(lead - base)
		if len(b) < 1+l {
			return 0, errors.New("rlp: 長度頭截斷")
		}
		return l, nil
	}
	// 回 (頭長, 內容長度)。
	longLen := func(base byte) (int, int, error) {
		h := int(lead - base)
		if len(b) < 1+h {
			return 0, 0, errors.New("rlp: 長度頭截斷")
		}
		n := new(big.Int).SetBytes(b[1 : 1+h]).Int64()
		if n < 0 || len(b) < 1+h+int(n) {
			return 0, 0, errors.New("rlp: 內容長度越界")
		}
		return h, int(n), nil
	}
	content, total := b, 1
	switch {
	case lead < 0x80: // 單 byte：自身即內容
		return rlpItem{raw: b[:1], data: b[:1]}, nil
	case lead <= 0xB7: // 短 bytes：長度 = lead-0x80
		l, err := shortLen(0x80)
		if err != nil {
			return rlpItem{}, err
		}
		content, total = b[1:1+l], 1+l
	case lead <= 0xBF: // 長 bytes：長度頭 = lead-0xB7
		h, n, err := longLen(0xB7)
		if err != nil {
			return rlpItem{}, err
		}
		content, total = b[1+h:1+h+n], 1+h+n
	case lead <= 0xF7: // 短 list：長度 = lead-0xC0
		l, err := shortLen(0xC0)
		if err != nil {
			return rlpItem{}, err
		}
		content, total = b[1:1+l], 1+l
	default: // 長 list：長度頭 = lead-0xF7
		h, n, err := longLen(0xF7)
		if err != nil {
			return rlpItem{}, err
		}
		content, total = b[1+h:1+h+n], 1+h+n
	}
	if lead >= 0xC0 {
		items, err := rlpDecodeItems(content)
		if err != nil {
			return rlpItem{}, err
		}
		return rlpItem{raw: body[:total], list: items}, nil
	}
	return rlpItem{raw: body[:total], data: content}, nil
}

// rlpDecodeItems 解碼一段 list 內容為多個 item。
func rlpDecodeItems(b []byte) ([]rlpItem, error) {
	var items []rlpItem
	for len(b) > 0 {
		it, err := rlpDecodeItem(b)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
		b = b[len(it.raw):]
	}
	return items, nil
}

// itemInt 把 item 轉為無號整數。
func (it rlpItem) itemInt() *big.Int {
	return new(big.Int).SetBytes(it.data)
}

// DecodeEthRawTx 解碼 EIP-155 類型 0 交易 raw bytes。
func DecodeEthRawTx(raw []byte) (*EthTx, error) {
	if len(raw) == 0 {
		return nil, errors.New("eth: 空交易")
	}
	if raw[0] == 0x02 {
		// EIP-1559 type 2：0x02 || RLP([chainId, nonce, maxPriorityFee, maxFee, gas, to, value, data, accessList, yParity, r, s])
		item, err := rlpDecodeItem(raw[1:])
		if err != nil {
			return nil, fmt.Errorf("eth: type2 RLP 解碼失敗: %w", err)
		}
		if item.list == nil || len(item.list) != 12 {
			return nil, fmt.Errorf("eth: type2 交易需 12 元素 list，got %d", len(item.list))
		}
		return &EthTx{
			TxType:         2,
			ChainID:        item.list[0].itemInt(),
			Nonce:          item.list[1].itemInt(),
			MaxPriorityFee: item.list[2].itemInt(),
			MaxFee:         item.list[3].itemInt(),
			Gas:            item.list[4].itemInt(),
			To:             item.list[5].data,
			Value:          item.list[6].itemInt(),
			Data:           item.list[7].data,
			V:              item.list[9].itemInt(), // yParity
			R:              item.list[10].itemInt(),
			S:              item.list[11].itemInt(),
			Raw:            raw,
		}, nil
	}
	item, err := rlpDecodeItem(raw)
	if err != nil {
		return nil, fmt.Errorf("eth: RLP 解碼失敗: %w", err)
	}
	if item.list == nil || len(item.list) != 9 {
		return nil, fmt.Errorf("eth: 交易需為 9 元素 list，got %d", len(item.list))
	}
	return &EthTx{
		Nonce:    item.list[0].itemInt(),
		GasPrice: item.list[1].itemInt(),
		Gas:      item.list[2].itemInt(),
		To:       item.list[3].data,
		Value:    item.list[4].itemInt(),
		Data:     item.list[5].data,
		V:        item.list[6].itemInt(),
		R:        item.list[7].itemInt(),
		S:        item.list[8].itemInt(),
		Raw:      raw,
	}, nil
}

// RecoverEthSignerType2 從 type 2 交易簽名恢復簽名者（yParity 即 recid 0/1）。
func RecoverEthSignerType2(payload []byte, r, s, yParity *big.Int) (*btcec.PublicKey, error) {
	if r == nil || s == nil || r.Sign() <= 0 || s.Sign() <= 0 {
		return nil, errors.New("eth: 無效 r/s")
	}
	recid := yParity.Int64()
	if recid < 0 || recid > 3 {
		return nil, fmt.Errorf("eth: 無效 yParity=%v", yParity)
	}
	digest := Keccak256(payload)
	compact := make([]byte, 65)
	compact[0] = 27 + byte(recid)
	r.FillBytes(compact[1:33])
	s.FillBytes(compact[33:65])
	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		return nil, fmt.Errorf("eth: 簽名恢復失敗: %w", err)
	}
	return pub, nil
}

// RecoverEthSigner 從簽名恢復簽名者公鑰；chainId=0 表示 v 為 27/28 格式。
func RecoverEthSigner(payload []byte, r, s, v *big.Int, chainID int64) (*btcec.PublicKey, error) {
	var recid byte
	if chainID > 0 {
		// EIP-155: v = 35 + 2*chainID + recid
		base := 35 + 2*chainID
		recid = byte(v.Int64() - base)
	} else {
		recid = byte(v.Int64() - 27)
	}
	if recid > 3 {
		return nil, fmt.Errorf("eth: 無效 v=%v", v)
	}
	if r == nil || s == nil || r.Sign() <= 0 || s.Sign() <= 0 {
		return nil, errors.New("eth: 無效 r/s")
	}
	digest := Keccak256(payload)
	compact := make([]byte, 65)
	compact[0] = 27 + recid
	r.FillBytes(compact[1:33])
	s.FillBytes(compact[33:65])
	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		return nil, fmt.Errorf("eth: 簽名恢復失敗: %w", err)
	}
	return pub, nil
}

// VerifyEthSigByRecover 以公鑰恢復比對驗證 eth 簽名（RecoverCompact 成功
// 且恢復出的公鑰與期望一致，即等效 ECDSA 驗證）。
func VerifyEthSigByRecover(expected *btcec.PublicKey, payload []byte, r, s *big.Int, v byte) bool {
	if r == nil || s == nil || r.Sign() <= 0 || s.Sign() <= 0 {
		return false
	}
	var recid byte
	switch {
	case v >= 35:
		// EIP-155: v = 35 + 2*chainID + recid → recid = (v-35) & 1。
		recid = (v - 35) & 0x01
	case v >= 27:
		recid = v - 27
	default:
		// eth 相容層內嵌格式：v 即 recid（0/1）。
		recid = v
	}
	if recid > 3 {
		return false
	}
	digest := Keccak256(payload)
	compact := make([]byte, 65)
	compact[0] = 27 + recid
	r.FillBytes(compact[1:33])
	s.FillBytes(compact[33:65])
	pub, _, err := ecdsa.RecoverCompact(compact, digest)
	if err != nil {
		return false
	}
	return pub.IsEqual(expected)
}

// VerifyEthTx 驗證 TAC 交易內嵌的 eth 簽名。
// pubkey 格式 "eth:<payloadhex>:<pubkeyhex>"；signature 格式 "eth:<r||s||v>"（130 hex）。
func VerifyEthTx(tx map[string]any) bool {
	pubkeyStr, _ := tx["pubkey"].(string)
	sigStr, _ := tx["signature"].(string)
	if !strings.HasPrefix(pubkeyStr, "eth:") || !strings.HasPrefix(sigStr, "eth:") {
		return false
	}
	parts := strings.Split(pubkeyStr[4:], ":")
	if len(parts) != 2 {
		return false
	}
	payload, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	pub, err := parsePubKeyHex(parts[1])
	if err != nil {
		return false
	}
	// signature = r(32)||s(32)||v(1)，130 hex 字元。
	sigBytes, err := hex.DecodeString(sigStr[4:])
	if err != nil || len(sigBytes) != 65 {
		return false
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:64])
	v := sigBytes[64]

	if !VerifyEthSigByRecover(pub, payload, r, s, v) {
		return false
	}
	// 恢復地址須與交易 from 一致（20 字節 hash160 → tx0）。
	digest := Keccak256(payload)
	recovered, _, err := ecdsa.RecoverCompact(
		append([]byte{27 + v&0x01}, append(pad32(r), pad32(s)...)...), digest)
	if err != nil {
		return false
	}
	fromHash := Hash160(recovered.SerializeCompressed())
	if Hash160ToAddress(fromHash) != getStringField(tx, "from") {
		return false
	}
	return true
}

// getStringField 取 map 字串欄位（缺失回空串）。
func getStringField(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// RecoverEthPrecompile 以太 ecrecover precompile（0x01）：msgHash 為 caller
// 提供之 32-byte hash（不再次 keccak），v 27/28，r/s 各 32 bytes。
// 成功回 32-byte left-padded 地址；失敗回 error（呼叫端依以太規範回空）。
func RecoverEthPrecompile(msgHash, v, r, s []byte) ([]byte, error) {
	if len(msgHash) != 32 || len(r) != 32 || len(s) != 32 || len(v) == 0 {
		return nil, errors.New("eth: ecrecover 輸入需 hash32+v32+r32+s32")
	}
	vv := new(big.Int).SetBytes(v)
	var recid byte
	switch vv.Int64() {
	case 27, 28:
		recid = byte(vv.Int64() - 27)
	default:
		return nil, errors.New("eth: ecrecover 無效 v")
	}
	compact := make([]byte, 65)
	compact[0] = 27 + recid
	copy(compact[1:33], r)
	copy(compact[33:65], s)
	pub, _, err := ecdsa.RecoverCompact(compact, msgHash)
	if err != nil {
		return nil, fmt.Errorf("eth: ecrecover 恢復失敗: %w", err)
	}
	out := make([]byte, 32)
	copy(out[12:], Hash160(pub.SerializeCompressed()))
	return out, nil
}
