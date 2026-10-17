package web

import (
	"encoding/hex"
	"math/big"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// transferSig 為 ERC-20/721 Transfer(address,address,uint256) 事件 topic0。
const transferSig = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// TransferView 是 Explorer 用的代幣轉移（ERC-20 數量 / ERC-721 tokenId）展示視圖。
type TransferView struct {
	Contract string `json:"contract"`
	From     string `json:"from"`
	To       string `json:"to"`
	Value    string `json:"value"`
	TokenID  string `json:"token_id"`
	Hash     string `json:"hash"`
	Block    int64  `json:"block"`
}

// evmTopicAddr 從 indexed topic（32 位元組）取出後 20 位元組地址。
func evmTopicAddr(t string) string {
	s := strings.TrimPrefix(strings.TrimSpace(t), "0x")
	if len(s) < 40 {
		return ""
	}
	return "0x" + s[len(s)-40:]
}

// hexDataToDec 把 32 位元組 hex data 解析為十進位（ERC-20 數量 / ERC-721 tokenId）。
func hexDataToDec(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return "0"
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return "0"
	}
	return new(big.Int).SetBytes(b).String()
}

// tx0ToEvmAddr 把 tx0 錢包地址轉為 EVM 0x 地址（與 node.tx0To0x 同源）。
func tx0ToEvmAddr(tx0 string) string {
	h, err := crypto.AddressToHash160(tx0)
	if err != nil {
		return ""
	}
	return "0x" + hex.EncodeToString(h)
}

// evmToTx0Addr 把 EVM 0x 地址轉回 tx0 錢包地址（與 node.evmToTx0 同源）。
func evmToTx0Addr(evm string) string {
	s := strings.TrimPrefix(strings.TrimSpace(evm), "0x")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 20 {
		return ""
	}
	return crypto.Hash160ToAddress(b)
}

// parseTransferViews 從日誌列中解析 ERC-20/721 Transfer 事件（topic0 大小寫不敏感）。
func parseTransferViews(rows []chaindb.LogRow) []TransferView {
	out := make([]TransferView, 0, len(rows))
	for i := range rows {
		if len(rows[i].Topics) == 0 {
			continue
		}
		if !strings.EqualFold(rows[i].Topics[0], transferSig) {
			continue
		}
		from, to := "", ""
		if len(rows[i].Topics) > 1 {
			from = evmTopicAddr(rows[i].Topics[1])
		}
		if len(rows[i].Topics) > 2 {
			to = evmTopicAddr(rows[i].Topics[2])
		}
		val := hexDataToDec(rows[i].Data)
		out = append(out, TransferView{
			Contract: rows[i].Address,
			From:     from,
			To:       to,
			Value:    val,
			TokenID:  val,
			Hash:  rows[i].TxHash,
			Block: rows[i].BlockHeight,
		})
	}
	return out
}

// transferViewDisplay 把 from/to/contract 的 0x 地址轉為系統 tx0 格式供 Explorer 顯示與鏈接。
func transferViewDisplay(vs []TransferView) []TransferView {
	for i := range vs {
		if strings.HasPrefix(vs[i].From, "0x") {
			vs[i].From = evmToTx0Addr(vs[i].From)
		}
		if strings.HasPrefix(vs[i].To, "0x") {
			vs[i].To = evmToTx0Addr(vs[i].To)
		}
		if strings.HasPrefix(vs[i].Contract, "0x") {
			vs[i].Contract = evmToTx0Addr(vs[i].Contract)
		}
	}
	return vs
}

// transfersForAddress 查詢該地址作為 from/to 的代幣轉移（topic0 過濾 + 記憶體地址比對）。
func transfersForAddress(ds DataSource, addrTx0 string, limit int) ([]TransferView, error) {
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	rows, err := ds.Logs(chaindb.LogFilter{
		Topics: [][]string{{transferSig}},
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	all := parseTransferViews(rows)
	addrEvm := tx0ToEvmAddr(addrTx0)
	out := make([]TransferView, 0, len(all))
	for _, tv := range all {
		if tv.From == addrEvm || tv.To == addrEvm {
			out = append(out, tv)
		}
	}
	return out, nil
}
