package web

import (
	"net/http"
	"strconv"
)

// handleWalletTxsAPI GET /api/wallet/txs?address=&offset=&limit=&dir=in|out
// M88：地址交易分頁/篩選（轉入/轉出/全部）。
func (s *Server) handleWalletTxsAPI(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("address")
	if addr == "" {
		writeJSON(w, map[string]any{"error": "address 必填"})
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	dir := r.URL.Query().Get("dir")
	if dir != "in" && dir != "out" {
		dir = ""
	}
	txs, err := s.ds.TxsByAddressPage(addr, offset, limit, dir)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"txs": txs, "offset": offset, "limit": limit, "dir": dir, "count": len(txs)})
}

// CountTxsByAddress 資料源：地址交易總數（dir 過濾）。
func (d *nodeDS) CountTxsByAddress(address, dir string) (int64, error) {
	return d.n.DB().CountTransactionsByAddress(address, dir)
}

// TxsByAddressPage 資料源：地址交易分頁（dir 過濾）。
func (d *nodeDS) TxsByAddressPage(address string, offset, limit int, dir string) ([]TxView, error) {
	rows, err := d.n.DB().GetTransactionsByAddressPage(address, offset, limit, dir)
	if err != nil {
		return nil, err
	}
	out := make([]TxView, 0, len(rows))
	for _, t := range rows {
		out = append(out, txView(t))
	}
	return out, nil
}

// CSV 匯出：同資料源全量（dir 過濾）轉成 CSV 字串。
func (d *nodeDS) TxsCSV(address, dir string) (string, error) {
	all, err := d.n.DB().GetTransactionsByAddressPage(address, 0, 5000, dir)
	if err != nil {
		return "", err
	}
	var b []byte
	b = append(b, "tx_hash,from,to,amount,fee,block_height,ts,type\n"...)
	for _, t := range all {
		typ := "send"
		if t.FromAddr == address {
			typ = "send"
		}
		if t.ToAddr == address && t.FromAddr != address {
			typ = "receive"
		}
		line := t.TxHash + "," + t.FromAddr + "," + t.ToAddr + "," + t.Amount + "," +
			t.Fee + "," + strconv.FormatInt(t.BlockHeight, 10) + "," +
			strconv.FormatInt(t.Ts, 10) + "," + typ + "\n"
		b = append(b, line...)
	}
	return string(b), nil
}

// handleWalletTxsCSV GET /api/wallet/txs/csv?address=&dir=
func (s *Server) handleWalletTxsCSV(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("address")
	if addr == "" {
		writeJSON(w, map[string]any{"error": "address 必填"})
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir != "in" && dir != "out" {
		dir = ""
	}
	csv, err := s.ds.TxsCSV(addr, dir)
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=tac_txs.csv")
	_, _ = w.Write([]byte(csv))
}
