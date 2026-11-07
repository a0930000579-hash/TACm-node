package community

// live.go — 直播功能：直播間（live_rooms）＋聊天串流（live_messages）。
//   開播者建立房間（status=live），觀眾輪詢聊天訊息；結束後 status=ended。
//   身份＝TAC 錢包地址（tx0 開頭），與動態牆同一身份體系。

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// LiveRoom 直播間視圖。
type LiveRoom struct {
	ID        int64  `json:"id"`
	Owner     string `json:"owner"`
	Title     string `json:"title"`
	Status    string `json:"status"` // live / ended
	CreatedAt int64  `json:"created_at"`
	Viewers   int64  `json:"viewers"` // 有發言過的人數（近似在線）
}

// LiveMessage 直播聊天訊息。
type LiveMessage struct {
	ID        int64  `json:"id"`
	RoomID    int64  `json:"room_id"`
	Address   string `json:"address"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

// CreateLiveRoom 開播：建立直播間。
func (s *Store) CreateLiveRoom(owner, title string) (int64, error) {
	owner = trimAddr(owner)
	title = trimTitle(title)
	if owner == "" {
		return 0, fmt.Errorf("community: 缺少開播者地址")
	}
	if title == "" {
		return 0, fmt.Errorf("community: 直播標題不能為空")
	}
	res, err := s.db.Exec(`INSERT INTO live_rooms(owner, title, status, created_at) VALUES(?,?,?,?)`,
		owner, title, "live", time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("community: 建立直播間: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("community: 讀取直播間 ID: %w", err)
	}
	return id, nil
}

// ListLiveRooms 列出直播間（live 在前）。
func (s *Store) ListLiveRooms(limit int) ([]LiveRoom, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT r.id, r.owner, r.title, r.status, r.created_at,
		       (SELECT COUNT(DISTINCT m.address) FROM live_messages m WHERE m.room_id = r.id) AS viewers
		FROM live_rooms r
		ORDER BY CASE WHEN r.status='live' THEN 0 ELSE 1 END, r.created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 查詢直播間: %w", err)
	}
	defer rows.Close()
	out := make([]LiveRoom, 0, 8)
	for rows.Next() {
		var r LiveRoom
		if err := rows.Scan(&r.ID, &r.Owner, &r.Title, &r.Status, &r.CreatedAt, &r.Viewers); err != nil {
			return nil, fmt.Errorf("community: 讀取直播間: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EndLiveRoom 結束直播。
func (s *Store) EndLiveRoom(roomID int64, owner string) error {
	res, err := s.db.Exec(`UPDATE live_rooms SET status='ended' WHERE id=? AND owner=? AND status='live'`,
		roomID, trimAddr(owner))
	if err != nil {
		return fmt.Errorf("community: 結束直播: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("community: 直播間不存在或非開播者")
	}
	return nil
}

// PostLiveMessage 發送聊天訊息（also 視為在線）。
func (s *Store) PostLiveMessage(roomID int64, address, body string) (int64, error) {
	address = trimAddr(address)
	body = trimBody(body)
	if address == "" {
		return 0, fmt.Errorf("community: 缺少地址")
	}
	if body == "" {
		return 0, fmt.Errorf("community: 訊息不能為空")
	}
	res, err := s.db.Exec(`INSERT INTO live_messages(room_id, address, body, created_at) VALUES(?,?,?,?)`,
		roomID, address, body, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("community: 發送訊息: %w", err)
	}
	return res.LastInsertId()
}

// LiveMessages 拉取直播間訊息（after 增量輪詢）。
func (s *Store) LiveMessages(roomID, after int64, limit int) ([]LiveMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, room_id, address, body, created_at FROM live_messages
		WHERE room_id=? AND id>?
		ORDER BY id ASC LIMIT ?`, roomID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("community: 查詢訊息: %w", err)
	}
	defer rows.Close()
	out := make([]LiveMessage, 0, 16)
	for rows.Next() {
		var m LiveMessage
		if err := rows.Scan(&m.ID, &m.RoomID, &m.Address, &m.Body, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("community: 讀取訊息: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LiveRoomByID 取單一直播間（含狀態）。
func (s *Store) LiveRoomByID(roomID int64) (LiveRoom, error) {
	var r LiveRoom
	err := s.db.QueryRow(`
		SELECT id, owner, title, status, created_at,
		       (SELECT COUNT(DISTINCT address) FROM live_messages WHERE room_id=live_rooms.id)
		FROM live_rooms WHERE id=?`, roomID).
		Scan(&r.ID, &r.Owner, &r.Title, &r.Status, &r.CreatedAt, &r.Viewers)
	if err == sql.ErrNoRows {
		return LiveRoom{}, fmt.Errorf("community: 直播間不存在")
	}
	if err != nil {
		return LiveRoom{}, fmt.Errorf("community: 讀取直播間: %w", err)
	}
	return r, nil
}

// trimAddr 清理地址輸入。
func trimAddr(v string) string { return strings.TrimSpace(v) }

// trimTitle 清理直播標題（限長）。
func trimTitle(v string) string {
	v = strings.TrimSpace(v)
	if len([]rune(v)) > 80 {
		v = string([]rune(v)[:80])
	}
	return v
}

// trimBody 清理聊天訊息（限長）。
func trimBody(v string) string {
	v = strings.TrimSpace(v)
	if len([]rune(v)) > 500 {
		v = string([]rune(v)[:500])
	}
	return v
}
