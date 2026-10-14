package chaindb

import (
	"path/filepath"
	"testing"
)

// TestLogsCRUD 驗證 M75-2：logs 表寫入／查詢（tx/address/topic0 過濾）與 gas_used 回寫。
func TestLogsCRUD(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 1. 寫入兩筆交易日誌。
	rows := []LogRow{
		{Address: "0xaaa", Topics: []string{"0xt1", "0xa1"}, Data: "0x11"},
		{Address: "0xbbb", Topics: []string{"0xt2"}, Data: "0x22"},
	}
	if err := db.InsertLogs("tx1", 5, 0, rows); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertLogs("tx2", 6, 1, []LogRow{{Address: "0xaaa", Topics: []string{"0xt1"}, Data: "0x33"}}); err != nil {
		t.Fatal(err)
	}

	// 2. 空插入不報錯。
	if err := db.InsertLogs("tx3", 7, 0, nil); err != nil {
		t.Fatal(err)
	}

	// 3. 依 tx_hash 查。
	byTx, err := db.GetLogs(LogFilter{TxHash: "tx1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTx) != 2 || byTx[0].Address != "0xaaa" || byTx[0].Topics[0] != "0xt1" {
		t.Fatalf("tx 過濾異常: %+v", byTx)
	}

	// 4. 依 address＋topic0 查（跨交易聚合）。
	byAddr, err := db.GetLogs(LogFilter{Address: "0xaaa", Topic0: "0xt1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAddr) != 2 {
		t.Fatalf("address+topic0 過濾應命中 2 筆，得到 %d", len(byAddr))
	}

	// 5. 依區塊範圍查。
	byBlock, err := db.GetLogs(LogFilter{FromBlock: 6, ToBlock: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(byBlock) != 1 || byBlock[0].TxHash != "tx2" {
		t.Fatalf("區塊範圍過濾異常: %+v", byBlock)
	}

	// 6. 升序（block/tx/log index）。
	all, err := db.GetLogs(LogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].BlockHeight != 5 || all[2].BlockHeight != 6 {
		t.Fatalf("升序異常: %+v", all)
	}

	// 7. UpdateTxGasUsed 回寫（transactions 表 migration 後可用）。
	if err := db.UpdateTxGasUsed("tx1", 42000); err != nil {
		t.Fatalf("UpdateTxGasUsed 失敗: %v", err)
	}
	// 未插入的交易也可回寫（UPDATE 不報錯）。
	if err := db.UpdateTxGasUsed("nosuch", 1); err != nil {
		t.Fatalf("UpdateTxGasUsed nosuch 失敗: %v", err)
	}
}

// TestLogLimit 驗證 GetLogs Limit 上限（≤10000 與 <=0 回退）。
func TestLogLimit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "logs2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 5; i++ {
		if err := db.InsertLogs("tx", 1, i, []LogRow{{Address: "0xa", Topics: []string{"0xt"}, Data: ""}}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := db.GetLogs(LogFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("Limit=2 應回 2 筆，得到 %d", len(out))
	}
	out0, err := db.GetLogs(LogFilter{Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(out0) != 5 {
		t.Fatalf("Limit=0 應回全部 5 筆，得到 %d", len(out0))
	}
}
