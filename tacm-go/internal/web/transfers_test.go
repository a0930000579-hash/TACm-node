package web

import (
	"strings"
	"testing"

	"tacm/internal/chaindb"
)

// TestParseTransferViews 驗證 ERC-20/721 Transfer 事件解析（topic0 過濾、topic 地址截取、data 數量）。
func TestParseTransferViews(t *testing.T) {
	rows := []chaindb.LogRow{
		{
			Address: "0x1111111111111111111111111111111111111111",
			Topics: []string{
				transferSig,
				"0x000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"0x000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
			Data:    "0x00000000000000000000000000000000000000000000000000000000000000fa",
			TxHash:  "0x" + strings.Repeat("ab", 32),
			BlockHeight: 7,
		},
		{
			// 非 Transfer 事件應被過濾。
			Address: "0x2222222222222222222222222222222222222222",
			Topics:  []string{"0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"},
			Data:    "0x00",
		},
	}
	vs := parseTransferViews(rows)
	if len(vs) != 1 {
		t.Fatalf("應解析 1 筆 Transfer，得到 %d", len(vs))
	}
	if vs[0].From != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("From 解析錯誤: %s", vs[0].From)
	}
	if vs[0].To != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("To 解析錯誤: %s", vs[0].To)
	}
	if vs[0].Value != "250" || vs[0].TokenID != "250" {
		t.Fatalf("Value/TokenID 解析錯誤: %s / %s", vs[0].Value, vs[0].TokenID)
	}
	if vs[0].Block != 7 {
		t.Fatalf("Block 錯誤: %d", vs[0].Block)
	}
}

// TestTransferViewDisplay 驗證 0x → tx0 顯示轉換。
func TestTransferViewDisplay(t *testing.T) {
	vs := []TransferView{{
		Contract: "0x1111111111111111111111111111111111111111",
		From:     "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
	out := transferViewDisplay(vs)
	if out[0].From == "" || out[0].To == "" || out[0].Contract == "" {
		t.Fatalf("tx0 轉換後不應為空: %+v", out[0])
	}
	if strings.HasPrefix(out[0].From, "0x") {
		t.Fatalf("From 應轉為 tx0 格式: %s", out[0].From)
	}
}
