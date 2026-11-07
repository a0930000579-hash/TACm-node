package node

// v130：鏈上拍賣（Auction）——賣方掛單、競價遞增、到期結算。
// 主流鏈同級能力（如 Ethereum 拍賣合約／NFT 競價）：支持 TACm/TiUSD/USDT
// 及鏈上任意資產出價；結算後 winner 以最高價成交。

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"tacm/internal/wallet"
)

// 拍賣狀態。
const (
	auctionOpen      = "open"      // 進行中（可出價）
	auctionSold      = "sold"      // 已成交（有得標者）
	auctionUnsold    = "unsold"    // 流拍（無出價）
	auctionCancelled = "cancelled" // 已取消
)

// Auction 鏈上拍賣主體。
type Auction struct {
	ID         int64  `json:"id"`
	Seller     string `json:"seller"`
	Item       string `json:"item"`
	Asset      string `json:"asset"`
	StartPrice string `json:"start_price"`
	EndHeight  int64  `json:"end_height"`
	Status     string `json:"status"`
	Winner     string `json:"winner"`
	FinalPrice string `json:"final_price"`
	Height     int64  `json:"height"`
	TxHash     string `json:"tx_hash"`
}

// Bid 拍賣出價。
type Bid struct {
	AuctionID int64  `json:"auction_id"`
	Bidder    string `json:"bidder"`
	Amount    string `json:"amount"`
	Height    int64  `json:"height"`
	TxHash    string `json:"tx_hash"`
}

// AuctionManager 拍賣管理器（SQLite 持久化）。
type AuctionManager struct {
	db *sql.DB
}

// NewAuctionManager 開啟（或建立）拍賣資料庫。
func NewAuctionManager(path string) (*AuctionManager, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("auction: 開啟資料庫: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auction: 連線: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS auctions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			seller TEXT NOT NULL,
			item TEXT NOT NULL,
			asset TEXT NOT NULL,
			start_price TEXT NOT NULL,
			end_height INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			winner TEXT NOT NULL DEFAULT '',
			final_price TEXT NOT NULL DEFAULT '',
			height INTEGER NOT NULL,
			tx_hash TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS auction_bids (
			auction_id INTEGER NOT NULL,
			bidder TEXT NOT NULL,
			amount TEXT NOT NULL,
			height INTEGER NOT NULL,
			tx_hash TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (auction_id, bidder)
		);
		CREATE INDEX IF NOT EXISTS idx_auction_status ON auctions(status);
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("auction: 建表: %w", err)
	}
	return &AuctionManager{db: db}, nil
}

// Close 關閉資料庫。
func (m *AuctionManager) Close() error { return m.db.Close() }

// parseAmount 解析十進制金額（最小單位整數），資產非法或金額非法回報錯。
// TACm 走 Big（1e18）；穩定幣走 I64（1e6）——兩者統一轉成 big.Int 字串比較。
func (m *AuctionManager) parseAmount(amount string, asset wallet.Asset) (*big.Int, error) {
	amt, err := wallet.NewAmount(amount, asset)
	if err != nil {
		return nil, err
	}
	if amt.Big != nil {
		return amt.Big, nil
	}
	return big.NewInt(amt.I64), nil
}

// CreateAuction 建立拍賣（回傳拍賣 ID）。
func (m *AuctionManager) CreateAuction(seller, item, asset, startPrice string, endHeight, height int64, txHash string) (int64, error) {
	seller = strings.TrimSpace(seller)
	item = strings.TrimSpace(item)
	asset = strings.TrimSpace(asset)
	if !validTACAddress(seller) {
		return 0, errors.New("auction: 賣方地址無效")
	}
	if item == "" {
		return 0, errors.New("auction: 拍賣品名稱為空")
	}
	ast, ok := wallet.NormalizeAsset(asset)
	if !ok {
		return 0, errors.New("auction: 資產類型無效: " + asset)
	}
	if endHeight <= height {
		return 0, fmt.Errorf("auction: 結束高度 %d 必須大於目前高度 %d", endHeight, height)
	}
	sp, err := m.parseAmount(startPrice, ast)
	if err != nil {
		return 0, fmt.Errorf("auction: 起標價: %w", err)
	}
	res, err := m.db.Exec(
		`INSERT INTO auctions (seller, item, asset, start_price, end_height, status, height, tx_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		seller, item, asset, sp.String(), endHeight, auctionOpen, height, txHash)
	if err != nil {
		return 0, fmt.Errorf("auction: 建立: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("auction: 取回 ID: %w", err)
	}
	return id, nil
}

// Bid 出價：金額必須嚴格高於目前最高價（首筆不低於起標價）。
func (m *AuctionManager) Bid(auctionID int64, bidder, amount string, height int64, txHash string) error {
	bidder = strings.TrimSpace(bidder)
	if !validTACAddress(bidder) {
		return errors.New("auction: 出價者地址無效")
	}
	auc, err := m.AuctionInfo(auctionID)
	if err != nil {
		return fmt.Errorf("auction: 讀取拍賣: %w", err)
	}
	if auc == nil {
		return errors.New("auction: 拍賣不存在")
	}
	if auc.Status != auctionOpen {
		return fmt.Errorf("auction: 拍賣已結束（狀態 %s）", auc.Status)
	}
	if height > auc.EndHeight {
		return fmt.Errorf("auction: 拍賣已到期（結束高度 %d）", auc.EndHeight)
	}
	ast, ok := wallet.NormalizeAsset(auc.Asset)
	if !ok {
		return errors.New("auction: 拍賣資產類型無效")
	}
	amt, err := m.parseAmount(amount, ast)
	if err != nil {
		return fmt.Errorf("auction: 出價金額: %w", err)
	}
	// 目前最高價：max(起標價, 既有最高出價)。
	maxAmt, _ := new(big.Int).SetString(auc.StartPrice, 10)
	if maxAmt == nil {
		return errors.New("auction: 起標價損壞")
	}
	top, err := m.TopBid(auctionID)
	if err != nil {
		return fmt.Errorf("auction: 讀取最高出價: %w", err)
	}
	if top != nil {
		cur, ok := new(big.Int).SetString(top.Amount, 10)
		if !ok {
			return errors.New("auction: 出價記錄損壞")
		}
		if cur.Cmp(maxAmt) > 0 {
			maxAmt = cur
		}
	}
	if amt.Cmp(maxAmt) <= 0 {
		return fmt.Errorf("auction: 出價必須高於目前最高價 %s（資產 %s）", maxAmt.String(), auc.Asset)
	}
	_, err = m.db.Exec(
		`INSERT INTO auction_bids (auction_id, bidder, amount, height, tx_hash)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(auction_id, bidder) DO UPDATE SET amount=excluded.amount, height=excluded.height, tx_hash=excluded.tx_hash`,
		auctionID, bidder, amt.String(), height, txHash)
	if err != nil {
		return fmt.Errorf("auction: 出價寫入: %w", err)
	}
	return nil
}

// TopBid 回傳拍賣目前最高出價（無出價回 nil）。
func (m *AuctionManager) TopBid(auctionID int64) (*Bid, error) {
	row := m.db.QueryRow(
		`SELECT auction_id, bidder, amount, height, tx_hash FROM auction_bids
		 WHERE auction_id = ? ORDER BY amount DESC, height ASC LIMIT 1`, auctionID)
	var b Bid
	if err := row.Scan(&b.AuctionID, &b.Bidder, &b.Amount, &b.Height, &b.TxHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("auction: 查詢最高出價: %w", err)
	}
	return &b, nil
}

// Settle 結算拍賣：僅在目前高度 ≥ 結束高度時可執行；最高出價者得標。
func (m *AuctionManager) Settle(auctionID, height int64) (*Auction, error) {
	auc, err := m.AuctionInfo(auctionID)
	if err != nil {
		return nil, fmt.Errorf("auction: 讀取拍賣: %w", err)
	}
	if auc == nil {
		return nil, errors.New("auction: 拍賣不存在")
	}
	if auc.Status != auctionOpen {
		return nil, fmt.Errorf("auction: 拍賣已結算（狀態 %s）", auc.Status)
	}
	if height < auc.EndHeight {
		return nil, fmt.Errorf("auction: 尚未到期（結束高度 %d，目前 %d）", auc.EndHeight, height)
	}
	top, err := m.TopBid(auctionID)
	if err != nil {
		return nil, err
	}
	if top == nil {
		if _, err := m.db.Exec(`UPDATE auctions SET status=?, height=? WHERE id=?`, auctionUnsold, height, auctionID); err != nil {
			return nil, fmt.Errorf("auction: 流拍寫入: %w", err)
		}
		auc.Status = auctionUnsold
		return auc, nil
	}
	if _, err := m.db.Exec(
		`UPDATE auctions SET status=?, winner=?, final_price=?, height=? WHERE id=?`,
		auctionSold, top.Bidder, top.Amount, height, auctionID); err != nil {
		return nil, fmt.Errorf("auction: 成交寫入: %w", err)
	}
	auc.Status, auc.Winner, auc.FinalPrice, auc.Height = auctionSold, top.Bidder, top.Amount, height
	return auc, nil
}

// AuctionInfo 讀取單一拍賣（不存在回 (nil, nil)）。
func (m *AuctionManager) AuctionInfo(auctionID int64) (*Auction, error) {
	row := m.db.QueryRow(
		`SELECT id, seller, item, asset, start_price, end_height, status, winner, final_price, height, tx_hash
		 FROM auctions WHERE id = ?`, auctionID)
	var a Auction
	if err := row.Scan(&a.ID, &a.Seller, &a.Item, &a.Asset, &a.StartPrice, &a.EndHeight,
		&a.Status, &a.Winner, &a.FinalPrice, &a.Height, &a.TxHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("auction: 查詢: %w", err)
	}
	return &a, nil
}

// Auctions 列出拍賣（status 為空時回傳全部；'open' 只列進行中）。
func (m *AuctionManager) Auctions(status string) ([]Auction, error) {
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = m.db.Query(`SELECT id, seller, item, asset, start_price, end_height, status, winner, final_price, height, tx_hash FROM auctions ORDER BY id DESC LIMIT 200`)
	} else {
		rows, err = m.db.Query(`SELECT id, seller, item, asset, start_price, end_height, status, winner, final_price, height, tx_hash FROM auctions WHERE status = ? ORDER BY id DESC LIMIT 200`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("auction: 列表: %w", err)
	}
	defer rows.Close()
	out := []Auction{}
	for rows.Next() {
		var a Auction
		if err := rows.Scan(&a.ID, &a.Seller, &a.Item, &a.Asset, &a.StartPrice, &a.EndHeight,
			&a.Status, &a.Winner, &a.FinalPrice, &a.Height, &a.TxHash); err != nil {
			return nil, fmt.Errorf("auction: 掃列: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Bids 列出拍賣的出價清單（金額降序）。
func (m *AuctionManager) Bids(auctionID int64) ([]Bid, error) {
	rows, err := m.db.Query(
		`SELECT auction_id, bidder, amount, height, tx_hash FROM auction_bids
		 WHERE auction_id = ? ORDER BY amount DESC`, auctionID)
	if err != nil {
		return nil, fmt.Errorf("auction: 出價列表: %w", err)
	}
	defer rows.Close()
	out := []Bid{}
	for rows.Next() {
		var b Bid
		if err := rows.Scan(&b.AuctionID, &b.Bidder, &b.Amount, &b.Height, &b.TxHash); err != nil {
			return nil, fmt.Errorf("auction: 掃列: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
