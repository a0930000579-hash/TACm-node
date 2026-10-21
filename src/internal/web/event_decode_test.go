package web

import (
	"strings"
	"testing"

	"tacm/internal/chaindb"
)

func lr(t0 string, topics []string, data string) chaindb.LogRow {
	return chaindb.LogRow{Address: "0xctr", Topics: append([]string{t0}, topics...), Data: data}
}

// TestDecodeTransfer 驗證 ERC-20 Transfer 解碼：from/to 取自 topics，value 取自 data。
func TestDecodeTransfer(t *testing.T) {
	from := "0x0000000000000000000000001111111111111111111111111111111111111111"
	to := "0x0000000000000000000000002222222222222222222222222222222222222222"
	data := "0x" + strings.Repeat("0", 63) + "f" // 15
	rows := []chaindb.LogRow{lr(topicTransfer, []string{from, to}, data)}
	evs := DecodeLogEvents(rows)
	if len(evs) != 1 {
		t.Fatalf("應解出 1 個事件，實際 %d", len(evs))
	}
	ev := evs[0]
	if ev.Name != "Transfer" {
		t.Fatalf("事件名錯誤: %s", ev.Name)
	}
	got := map[string]string{}
	for _, p := range ev.Params {
		got[p.Name] = p.Value
	}
	if got["from"] != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("from 解碼錯誤: %s", got["from"])
	}
	if got["to"] != "0x2222222222222222222222222222222222222222" {
		t.Fatalf("to 解碼錯誤: %s", got["to"])
	}
	if got["value"] != "15" {
		t.Fatalf("value 解碼錯誤: %s", got["value"])
	}
}

// TestDecodeApprovalForAll 驗證 bool 參數解碼（approved=true）。
func TestDecodeApprovalForAll(t *testing.T) {
	owner := "0x000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	op := "0x000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	data := "0x" + strings.Repeat("0", 63) + "1" // true
	evs := DecodeLogEvents([]chaindb.LogRow{lr(topicApprovalForAll, []string{owner, op}, data)})
	if len(evs) != 1 {
		t.Fatalf("ApprovalForAll 應解出 1 個，實際 %d", len(evs))
	}
	got := map[string]string{}
	for _, p := range evs[0].Params {
		got[p.Name] = p.Value
	}
	if got["approved"] != "true" {
		t.Fatalf("approved 應為 true，實際 %s", got["approved"])
	}
}

// TestDecodeTransferBatch 驗證動態 uint256[] 解碼（兩個動態參數各自 offset）。
func TestDecodeTransferBatch(t *testing.T) {
	// data: head=[0x40(ids offset), 0xa0(values offset)]，尾部 ids=[1,2]、values=[3,4]。
	data := "0x" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"00000000000000000000000000000000000000000000000000000000000000a0" +
		"0000000000000000000000000000000000000000000000000000000000000002" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000002" +
		"0000000000000000000000000000000000000000000000000000000000000002" +
		"0000000000000000000000000000000000000000000000000000000000000003" +
		"0000000000000000000000000000000000000000000000000000000000000004"
	evs := DecodeLogEvents([]chaindb.LogRow{lr(topicTransferBatch, []string{
		"0x000000000000000000000000cccccccccccccccccccccccccccccccccccccccc",
		"0x0000000000000000000000001111111111111111111111111111111111111111",
		"0x0000000000000000000000002222222222222222222222222222222222222222",
	}, data)})
	if len(evs) != 1 {
		t.Fatalf("TransferBatch 應解出 1 個，實際 %d", len(evs))
	}
	got := map[string]string{}
	for _, p := range evs[0].Params {
		got[p.Name] = p.Value
	}
	if got["ids"] != "[1,2]" {
		t.Fatalf("ids 解碼錯誤: %s", got["ids"])
	}
	if got["values"] != "[3,4]" {
		t.Fatalf("values 解碼錯誤: %s", got["values"])
	}
}

// TestDecodeUnknownIgnored 驗證未知 topic0 被略過（不產生假事件）。
func TestDecodeUnknownIgnored(t *testing.T) {
	rows := []chaindb.LogRow{lr("0xdeadbeef00000000000000000000000000000000000000000000000000000000", nil, "0x00")}
	if evs := DecodeLogEvents(rows); len(evs) != 0 {
		t.Fatalf("未知事件應被略過，實際解出 %d", len(evs))
	}
}
