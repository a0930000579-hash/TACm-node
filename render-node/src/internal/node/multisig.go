package node

// v129：鏈上多簽錢包（Gnosis Safe 模式）——N-of-M 簽名門檻批准後才可執行轉帳。
// 資料庫獨立（multisig.db）：wallets（owner 集合＋門檻）＋ms_txs（待執行提案）＋
// ms_confirmations（owner 確認）。執行時由 RPC 層注入 execFn（walletSvc.Transfer）。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// 多簽交易狀態。
const (
	msStatusPending   = "pending"
	msStatusExecuted  = "executed"
	msStatusCancelled = "cancelled"
)

// MultisigWallet 多簽錢包：一個地址、多個 owner、執行門檻。
type MultisigWallet struct {
	Address   string   `json:"address"`
	Owners    []string `json:"owners"`
	Threshold int      `json:"threshold"`
	CreatedAt int64    `json:"created_at"`
	TxHash    string   `json:"tx_hash"`
}

// MultisigTx 多簽交易提案（submit → confirm → execute）。
type MultisigTx struct {
	ID            int64  `json:"id"`
	Wallet        string `json:"wallet"`
	To            string `json:"to"`
	Asset         string `json:"asset"`
	Amount        string `json:"amount"`
	Memo          string `json:"memo"`
	Status        string `json:"status"`
	By            string `json:"by"`
	Height        int64  `json:"height"`
	CreatedAt     int64  `json:"created_at"`
	Confirmations int    `json:"confirmations"`
	ExecTxHash    string `json:"exec_tx_hash,omitempty"`
}

// MultisigManager 多簽錢包管理器（Goroutine 安全）。
type MultisigManager struct {
	db *sql.DB
	mu sync.Mutex
}

// NewMultisigManager 開啟/建立多簽資料庫。
func NewMultisigManager(dbPath string) (*MultisigManager, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("multisig: 開啟資料庫失敗: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("multisig: 資料庫連線失敗: %w", err)
	}
	m := &MultisigManager{db: db}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS wallets (
			address TEXT PRIMARY KEY,
			owners TEXT NOT NULL,
			threshold INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			tx_hash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS ms_txs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			wallet TEXT NOT NULL,
			to_addr TEXT NOT NULL,
			asset TEXT NOT NULL,
			amount TEXT NOT NULL,
			memo TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			by TEXT NOT NULL,
			height INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			exec_tx_hash TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS ms_confirmations (
			tx_id INTEGER NOT NULL,
			owner TEXT NOT NULL,
			height INTEGER NOT NULL,
			PRIMARY KEY(tx_id, owner)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return nil, fmt.Errorf("multisig: 初始化資料表失敗: %w", err)
		}
	}
	return m, nil
}

// Close 關閉資料庫。
func (m *MultisigManager) Close() error { return m.db.Close() }

// CreateWallet 建立多簽錢包（address 為錢包地址，owners 須非空、threshold 1..len(owners)）。
func (m *MultisigManager) CreateWallet(addr string, owners []string, threshold int, height int64, txHash string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("multisig: 錢包地址不能為空")
	}
	owners = cleanOwners(owners)
	if len(owners) < 1 {
		return errors.New("multisig: 至少需要 1 個 owner")
	}
	if threshold < 1 || threshold > len(owners) {
		return fmt.Errorf("multisig: 門檻需在 1..%d 之間", len(owners))
	}
	oj, err := json.Marshal(owners)
	if err != nil {
		return fmt.Errorf("multisig: 序列化 owners 失敗: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := height // height 作為建立高度（鏈上時間參考）
	var exists string
	if err := m.db.QueryRow(`SELECT address FROM wallets WHERE address=?`, addr).Scan(&exists); err == nil {
		return errors.New("multisig: 錢包已存在")
	}
	_, err = m.db.Exec(
		`INSERT INTO wallets (address, owners, threshold, created_at, tx_hash) VALUES (?,?,?,?,?)`,
		addr, string(oj), threshold, now, txHash,
	)
	if err != nil {
		return fmt.Errorf("multisig: 建立錢包失敗: %w", err)
	}
	return nil
}

// Wallets 回傳全部多簽錢包。
func (m *MultisigManager) Wallets() ([]MultisigWallet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT address, owners, threshold, created_at, tx_hash FROM wallets ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MultisigWallet, 0)
	for rows.Next() {
		w := MultisigWallet{}
		var oj string
		if err := rows.Scan(&w.Address, &oj, &w.Threshold, &w.CreatedAt, &w.TxHash); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(oj), &w.Owners)
		out = append(out, w)
	}
	return out, nil
}

// WalletInfo 回傳單一錢包；不存在回 nil。
func (m *MultisigManager) WalletInfo(addr string) (*MultisigWallet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var oj string
	w := &MultisigWallet{}
	err := m.db.QueryRow(
		`SELECT address, owners, threshold, created_at, tx_hash FROM wallets WHERE address=?`,
		addr,
	).Scan(&w.Address, &oj, &w.Threshold, &w.CreatedAt, &w.TxHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(oj), &w.Owners); err != nil {
		return nil, err
	}
	return w, nil
}

// IsOwner 檢查地址是否為錢包 owner。
func (m *MultisigManager) IsOwner(addr, owner string) (bool, error) {
	w, err := m.WalletInfo(addr)
	if err != nil {
		return false, err
	}
	if w == nil {
		return false, errors.New("multisig: 錢包不存在")
	}
	for _, o := range w.Owners {
		if strings.EqualFold(o, owner) {
			return true, nil
		}
	}
	return false, nil
}

// SubmitTx 提交多簽轉帳提案（提交者須為 owner；回傳提案 id）。
func (m *MultisigManager) SubmitTx(wallet, to, asset, amount, memo, by string, height int64) (int64, error) {
	wallet = strings.TrimSpace(wallet)
	to = strings.TrimSpace(to)
	by = strings.TrimSpace(by)
	amount = strings.TrimSpace(amount)
	if wallet == "" || to == "" || by == "" || amount == "" {
		return 0, errors.New("multisig: 參數不能為空")
	}
	ok, err := m.IsOwner(wallet, by)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("multisig: 提交者不是錢包 owner")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.db.Exec(
		`INSERT INTO ms_txs (wallet, to_addr, asset, amount, memo, status, by, height, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		wallet, to, asset, amount, memo, msStatusPending, by, height, height,
	)
	if err != nil {
		return 0, fmt.Errorf("multisig: 提交交易失敗: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ConfirmTx 確認多簽交易（owner 可重複確認即重設無效，僅計一次；回傳目前確認數）。
func (m *MultisigManager) ConfirmTx(txID int64, by string, height int64) (int, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return 0, errors.New("multisig: 確認者不能為空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var wallet string
	if err := m.db.QueryRow(`SELECT wallet FROM ms_txs WHERE id=?`, txID).Scan(&wallet); err != nil {
		if err == sql.ErrNoRows {
			return 0, errors.New("multisig: 提案不存在")
		}
		return 0, err
	}
	if _, err := m.db.Exec(
		`INSERT OR IGNORE INTO ms_confirmations (tx_id, owner, height) VALUES (?,?,?)`,
		txID, by, height,
	); err != nil {
		return 0, fmt.Errorf("multisig: 確認失敗: %w", err)
	}
	var n int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM ms_confirmations WHERE tx_id=?`, txID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// ExecTxFn 執行回調：wallet→to 轉帳（由 RPC 層注入 walletSvc.Transfer）。
type ExecTxFn func(wallet, to, asset, amount, memo string) (string, error)

// ExecuteTx 執行多簽交易（確認數達門檻且提案 pending 才可執行；回傳完整提案）。
func (m *MultisigManager) ExecuteTx(txID int64, by string, execFn ExecTxFn) (*MultisigTx, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, errors.New("multisig: 執行者不能為空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.txByIDLocked(txID)
	if err != nil {
		return nil, err
	}
	if tx.Status != msStatusPending {
		return nil, fmt.Errorf("multisig: 提案已 %s，無法執行", tx.Status)
	}
	w, err := m.walletByAddrLocked(tx.Wallet)
	if err != nil {
		return nil, err
	}
	ok, err := m.isOwnerLocked(w, by)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("multisig: 執行者不是錢包 owner")
	}
	var n int
	if err := m.db.QueryRow(`SELECT COUNT(*) FROM ms_confirmations WHERE tx_id=?`, txID).Scan(&n); err != nil {
		return nil, err
	}
	if n < w.Threshold {
		return nil, fmt.Errorf("multisig: 確認數不足（%d/%d）", n, w.Threshold)
	}
	if execFn == nil {
		return nil, errors.New("multisig: 執行器未註冊")
	}
	execHash, err := execFn(tx.Wallet, tx.To, tx.Asset, tx.Amount, tx.Memo)
	if err != nil {
		return nil, fmt.Errorf("multisig: 執行轉帳失敗: %w", err)
	}
	if _, err := m.db.Exec(
		`UPDATE ms_txs SET status=?, exec_tx_hash=? WHERE id=?`,
		msStatusExecuted, execHash, txID,
	); err != nil {
		return nil, err
	}
	tx.Status = msStatusExecuted
	tx.ExecTxHash = execHash
	tx.Confirmations = n
	return tx, nil
}

// CancelTx 取消多簽交易（提案提交者可取消 pending 提案）。
func (m *MultisigManager) CancelTx(txID int64, by string) error {
	by = strings.TrimSpace(by)
	if by == "" {
		return errors.New("multisig: 取消者不能為空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tx, err := m.txByIDLocked(txID)
	if err != nil {
		return err
	}
	if tx.Status != msStatusPending {
		return fmt.Errorf("multisig: 提案已 %s，無法取消", tx.Status)
	}
	if tx.By != by {
		return errors.New("multisig: 只有提交者可取消提案")
	}
	if _, err := m.db.Exec(`UPDATE ms_txs SET status=? WHERE id=?`, msStatusCancelled, txID); err != nil {
		return err
	}
	return nil
}

// Txs 回傳錢包的全部交易提案（新→舊）。
func (m *MultisigManager) Txs(wallet string) ([]MultisigTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.txsLocked(wallet)
}

func (m *MultisigManager) txsLocked(wallet string) ([]MultisigTx, error) {
	rows, err := m.db.Query(
		`SELECT id, wallet, to_addr, asset, amount, memo, status, by, height, created_at, exec_tx_hash
		 FROM ms_txs WHERE wallet=? ORDER BY id DESC`,
		wallet,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MultisigTx, 0)
	for rows.Next() {
		tx := MultisigTx{}
		if err := rows.Scan(&tx.ID, &tx.Wallet, &tx.To, &tx.Asset, &tx.Amount, &tx.Memo,
			&tx.Status, &tx.By, &tx.Height, &tx.CreatedAt, &tx.ExecTxHash); err != nil {
			return nil, err
		}
		var n int
		_ = m.db.QueryRow(`SELECT COUNT(*) FROM ms_confirmations WHERE tx_id=?`, tx.ID).Scan(&n)
		tx.Confirmations = n
		out = append(out, tx)
	}
	return out, nil
}

// TxConfirmations 回傳提案的確認者清單。
func (m *MultisigManager) TxConfirmations(txID int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT owner FROM ms_confirmations WHERE tx_id=? ORDER BY height`, txID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (m *MultisigManager) txByIDLocked(txID int64) (*MultisigTx, error) {
	tx := &MultisigTx{}
	err := m.db.QueryRow(
		`SELECT id, wallet, to_addr, asset, amount, memo, status, by, height, created_at, exec_tx_hash
		 FROM ms_txs WHERE id=?`,
		txID,
	).Scan(&tx.ID, &tx.Wallet, &tx.To, &tx.Asset, &tx.Amount, &tx.Memo,
		&tx.Status, &tx.By, &tx.Height, &tx.CreatedAt, &tx.ExecTxHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("multisig: 提案不存在")
		}
		return nil, err
	}
	var n int
	_ = m.db.QueryRow(`SELECT COUNT(*) FROM ms_confirmations WHERE tx_id=?`, txID).Scan(&n)
	tx.Confirmations = n
	return tx, nil
}

func (m *MultisigManager) walletByAddrLocked(addr string) (*MultisigWallet, error) {
	var oj string
	w := &MultisigWallet{}
	err := m.db.QueryRow(
		`SELECT address, owners, threshold, created_at, tx_hash FROM wallets WHERE address=?`,
		addr,
	).Scan(&w.Address, &oj, &w.Threshold, &w.CreatedAt, &w.TxHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.New("multisig: 錢包不存在")
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(oj), &w.Owners); err != nil {
		return nil, err
	}
	return w, nil
}

func (m *MultisigManager) isOwnerLocked(w *MultisigWallet, owner string) (bool, error) {
	if w == nil {
		return false, errors.New("multisig: 錢包不存在")
	}
	for _, o := range w.Owners {
		if strings.EqualFold(o, owner) {
			return true, nil
		}
	}
	return false, nil
}

func cleanOwners(owners []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(owners))
	for _, o := range owners {
		o = strings.TrimSpace(o)
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out
}
