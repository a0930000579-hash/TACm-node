package web

import (
	"encoding/hex"
	"math/big"
	"strings"

	"tacm/internal/chaindb"
)

// 已知事件簽名 topic0（以 Keccak256 實算，見 internal/crypto 測試）。
const (
	topicTransfer       = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	topicApproval       = "0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"
	topicApprovalForAll = "0x17307eab39ab6107e8899845ad3d59bd9653f200f220920489ca2b5937696c31"
	topicTransferSingle = "0xc3d58168c5ae7397731d063d5bbf3d657854427343f4c083240f7aacaa2d0f62"
	topicTransferBatch  = "0x4a39dc06d4c0dbc64b70af90fd698a233a518aa5d07e595d983b8c0526c8f7fb"
)

// EventParam 為解碼後的事件參數（含參數名，供 Explorer 人類可讀顯示）。
type EventParam struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DecodedEvent 為一筆日誌解碼後的已知事件（未知 topic0 由調用方略過）。
type DecodedEvent struct {
	Name    string       `json:"name"`
	Address string       `json:"address"`
	Params  []EventParam `json:"params"`
}

// eventSpec 描述事件的 indexed（topics[1..]）與 data 區參數（名＋型別）。
type eventSpec struct {
	name      string
	indexed   []string // topics[1] 起對應的參數名
	dataNames []string // data 區參數名（與 dataTypes 一一對應）
	dataTypes []string // data 區參數型別：uint256 | bool | uint256[]
}

var knownEvents = map[string]eventSpec{
	topicTransfer:       {name: "Transfer", indexed: []string{"from", "to"}, dataNames: []string{"value"}, dataTypes: []string{"uint256"}},
	topicApproval:       {name: "Approval", indexed: []string{"owner", "spender"}, dataNames: []string{"value"}, dataTypes: []string{"uint256"}},
	topicApprovalForAll: {name: "ApprovalForAll", indexed: []string{"owner", "operator"}, dataNames: []string{"approved"}, dataTypes: []string{"bool"}},
	topicTransferSingle: {name: "TransferSingle", indexed: []string{"operator", "from", "to"}, dataNames: []string{"id", "value"}, dataTypes: []string{"uint256", "uint256"}},
	topicTransferBatch:  {name: "TransferBatch", indexed: []string{"operator", "from", "to"}, dataNames: []string{"ids", "values"}, dataTypes: []string{"uint256[]", "uint256[]"}},
}

// evmWordAddr 從 32 位元組 topic 取後 20 位元組地址。
func evmWordAddr(w string) string {
	s := strings.TrimPrefix(strings.TrimSpace(w), "0x")
	if len(s) < 40 {
		return ""
	}
	return "0x" + s[len(s)-40:]
}

// evmWordUint 把 32 位元組 word 轉十進位。
func evmWordUint(w string) string {
	s := strings.TrimPrefix(strings.TrimSpace(w), "0x")
	if s == "" {
		return "0"
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return "0"
	}
	return new(big.Int).SetBytes(b).String()
}

// evmWordBool 讀 word 最後一 byte 判定布林。
func evmWordBool(w string) bool {
	s := strings.TrimPrefix(strings.TrimSpace(w), "0x")
	if s == "" {
		return false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) == 0 {
		return false
	}
	return b[len(b)-1] != 0
}

// evmDecodeUint256ArrayAt 解析 ABI 動態 uint256[]（data 區第 dynamicIdx 個動態參數：
// head 中該參數的 offset word → len word → 元素 words）。
func evmDecodeUint256ArrayAt(dataHex string, dynamicIdx int) ([]string, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(dataHex), "0x")
	words := splitWords(s)
	if len(words) <= dynamicIdx {
		return nil, false
	}
	offWord := evmWordUint(words[dynamicIdx])
	off, err := bigWordToIndex(offWord)
	if err != nil || off%32 != 0 || off/32 < 1 || off/32 >= len(words) {
		return nil, false
	}
	idx := off / 32
	length := evmWordUint(words[idx])
	cnt, err := bigWordToIndex(length)
	if err != nil || cnt < 0 || idx+1+cnt > len(words) {
		return nil, false
	}
	out := make([]string, 0, cnt)
	for i := 0; i < cnt; i++ {
		out = append(out, evmWordUint(words[idx+1+i]))
	}
	return out, true
}

// splitWords 把連續 hex 切成 32 位元組 words。
func splitWords(hexStr string) []string {
	if len(hexStr)%64 != 0 {
		// 不完整 word：補 0 到 64 倍數，避免誤解。
		return nil
	}
	out := make([]string, 0, len(hexStr)/64)
	for i := 0; i+64 <= len(hexStr); i += 64 {
		out = append(out, hexStr[i:i+64])
	}
	return out
}

// bigWordToIndex 把 word 十進位字串轉為 int 索引。
func bigWordToIndex(s string) (int, error) {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || !n.IsInt64() {
		return 0, errNotIndex
	}
	v := n.Int64()
	if v < 0 || v > 1<<20 {
		return 0, errNotIndex
	}
	return int(v), nil
}

var errNotIndex = errString("not a valid array index")

type errString string

func (e errString) Error() string { return string(e) }

// DecodeLogEvents 把日誌列解碼為已知事件（未知 topic0 略過，返回 nil 表示無可解碼）。
func DecodeLogEvents(rows []chaindb.LogRow) []DecodedEvent {
	out := make([]DecodedEvent, 0, len(rows))
	for i := range rows {
		ev, ok := decodeOneEvent(rows[i])
		if !ok {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// decodeOneEvent 解碼單筆日誌；未知事件回 ok=false。
func decodeOneEvent(row chaindb.LogRow) (DecodedEvent, bool) {
	if len(row.Topics) == 0 {
		return DecodedEvent{}, false
	}
	spec, known := knownEvents[strings.ToLower(row.Topics[0])]
	if !known {
		return DecodedEvent{}, false
	}
	ev := DecodedEvent{Name: spec.name, Address: row.Address}
	// indexed 參數（topics[1..]）。
	for j, name := range spec.indexed {
		if len(row.Topics) > j+1 {
			ev.Params = append(ev.Params, EventParam{Name: name, Value: evmWordAddr(row.Topics[j+1])})
		}
	}
	// data 區參數（動態陣列依其在 head 中的序號解碼）。
	words := splitWords(strings.TrimPrefix(strings.TrimSpace(row.Data), "0x"))
	wi := 0
	dynIdx := 0
	for di, dt := range spec.dataTypes {
		name := spec.dataNames[di]
		switch dt {
		case "uint256":
			if wi < len(words) {
				ev.Params = append(ev.Params, EventParam{Name: name, Value: evmWordUint(words[wi])})
				wi++
			}
		case "bool":
			if wi < len(words) {
				ev.Params = append(ev.Params, EventParam{Name: name, Value: boolStr(evmWordBool(words[wi]))})
				wi++
			}
		case "uint256[]":
			if vals, ok := evmDecodeUint256ArrayAt(row.Data, dynIdx); ok {
				ev.Params = append(ev.Params, EventParam{Name: name, Value: "[" + strings.Join(vals, ",") + "]"})
			}
			dynIdx++
		}
	}
	return ev, true
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
