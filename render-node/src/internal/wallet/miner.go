package wallet

// miner.go — 礦機模組（對齊 Python autominers/miner_client 原本方式）：
//   礦機以地址註冊（VCPU/VGPU 算力）→ 週期心跳 → 出塊時 coinbase 剩餘
//   按在線礦工算力占比瓜分（PoolShareBps 進獎勵池、其餘 100% 依算力分配）。

import (
	"database/sql"
	"fmt"
	"math/big"
	"strconv"
	"time"
)

// MinerOnlineWindow 心跳有效窗口：超過此秒數未心跳視為離線，不參與瓜分。
const MinerOnlineWindow = 180

const minerSchema = `
CREATE TABLE IF NOT EXISTS miners (
    address TEXT PRIMARY KEY,
    vcpu    REAL NOT NULL DEFAULT 1,
    vgpu    REAL NOT NULL DEFAULT 2,
    hashrate TEXT NOT NULL DEFAULT '0',
    status  TEXT NOT NULL DEFAULT 'online',
    last_heartbeat_ts INTEGER NOT NULL DEFAULT 0,
    registered_ts INTEGER NOT NULL,
    active INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS referrals (
    referrer TEXT NOT NULL,
    referral TEXT NOT NULL,
    ts       INTEGER NOT NULL,
    PRIMARY KEY (referrer, referral)
);
`

// Miner 礦機視圖。
type Miner struct {
	Address         string  `json:"address"`
	VCPU            float64 `json:"vcpu"`
	VGPU            float64 `json:"vgpu"`
	Hashrate        string  `json:"hashrate"`
	Status          string  `json:"status"`
	LastHeartbeatTs int64   `json:"last_heartbeat_ts"`
	RegisteredTs    int64   `json:"registered_ts"`
	Online          bool    `json:"online"`
	Active          bool    `json:"active"`
}

// BaseCPU/BaseGPU：礦機預設算力（不可手動修改，M32 固定）。
// 算力提升唯一途徑＝推薦註冊：每 1 名推薦 +0.01 CPU、+0.02 GPU。
const (
	BaseCPU = 1.0
	BaseGPU = 2.0
	RefCPU  = 0.01
	RefGPU  = 0.02
)

// MinerHashrate 回傳指定地址已註冊礦機的算力（hashrate 欄位；未註冊回 0）。
// M60：鏈上 hb 交易需要攜帶礦機算力（開機/續心跳），算力由本節點已註冊礦機表提供。
func (s *Store) MinerHashrate(address string) (int64, error) {
	var hr string
	err := s.db.QueryRow(`SELECT hashrate FROM miners WHERE address=?`, address).Scan(&hr)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("wallet: 讀取礦機算力: %w", err)
	}
	v, err := strconv.ParseInt(hr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("wallet: 礦機算力格式錯誤: %w", err)
	}
	return v, nil
}

// RegisterMiner 註冊（或更新）一台礦機：算力固定 = 基礎 1vCPU+2vGPU＋推薦加成。
// vcpu/vgpu 參數被忽略（防止手動改算力），僅為向後相容保留。
func (s *Store) RegisterMiner(address string, _vcpu, _vgpu int) error {
	if address == "" {
		return fmt.Errorf("wallet: 礦機註冊參數非法 (address=%q)", address)
	}
	n, err := s.ReferralCount(address)
	if err != nil {
		return err
	}
	vcpu := BaseCPU + float64(n)*RefCPU
	vgpu := BaseGPU + float64(n)*RefGPU
	hr := int64(vcpu*1_000_000 + vgpu*2_000_000)
	now := time.Now().Unix()
	_, err = s.db.Exec(`
INSERT INTO miners (address, vcpu, vgpu, hashrate, status, last_heartbeat_ts, registered_ts)
VALUES (?, ?, ?, ?, 'online', ?, ?)
ON CONFLICT(address) DO UPDATE SET
  vcpu=excluded.vcpu, vgpu=excluded.vgpu, hashrate=excluded.hashrate,
  status='online', last_heartbeat_ts=excluded.last_heartbeat_ts`,
		address, vcpu, vgpu, fmt.Sprintf("%d", hr), now, now)
	if err != nil {
		return fmt.Errorf("wallet: 註冊礦機: %w", err)
	}
	return nil
}

// AddReferral 記錄一次推薦註冊（referrer 為邀請人地址，referral 為新註冊地址）。
func (s *Store) AddReferral(referrer, referral string) error {
	if referrer == "" || referral == "" || referrer == referral {
		return fmt.Errorf("wallet: 推薦關係非法")
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO referrals (referrer, referral, ts) VALUES (?, ?, ?)`,
		referrer, referral, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("wallet: 記錄推薦: %w", err)
	}
	return nil
}

// ReferralCount 回傳指定地址的推薦註冊人數。
func (s *Store) ReferralCount(referrer string) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM referrals WHERE referrer=?`, referrer).Scan(&n); err != nil {
		return 0, fmt.Errorf("wallet: 查詢推薦數: %w", err)
	}
	return n, nil
}

// MinerSplit 一個礦工的瓜分份額（Share 為分子，分母為全體算力和）。
type MinerSplit struct {
	Address string   `json:"address"`
	Share   *big.Int `json:"share"`
}

// TickMiner 礦機心跳：更新最後心跳時間並維持在線。
func (s *Store) TickMiner(address string) error {
	if address == "" {
		return fmt.Errorf("wallet: 心跳缺少地址")
	}
	res, err := s.db.Exec(`UPDATE miners SET status='online', last_heartbeat_ts=? WHERE address=?`,
		time.Now().Unix(), address)
	if err != nil {
		return fmt.Errorf("wallet: 礦機心跳: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("wallet: 礦機未註冊（請先 /api/miner/register）: %s", address)
	}
	return nil
}

// StopMiner 礦機離線（登出/停止）：M33 關閉後端排程並將心跳歸零，前端訊號即時轉灰。
func (s *Store) StopMiner(address string) error {
	_, err := s.db.Exec(`UPDATE miners SET status='offline', last_heartbeat_ts=0, active=0 WHERE address=?`, address)
	if err != nil {
		return fmt.Errorf("wallet: 礦機離線: %w", err)
	}
	return nil
}

// SetActiveMiner 開啟/關閉伺服器端挖礦排程（M33：離開頁面仍持續挖礦）。
// active=true：設為啟用並立即心跳（訊號即時轉綠）。
func (s *Store) SetActiveMiner(address string, active bool) error {
	if address == "" {
		return fmt.Errorf("wallet: 啟用礦機缺少地址")
	}
	if active {
		res, err := s.db.Exec(`UPDATE miners SET active=1, status='online', last_heartbeat_ts=? WHERE address=?`,
			time.Now().Unix(), address)
		if err != nil {
			return fmt.Errorf("wallet: 啟用礦機: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("wallet: 礦機未註冊（請先 /api/miner/register）: %s", address)
		}
		return nil
	}
	_, err := s.db.Exec(`UPDATE miners SET active=0, last_heartbeat_ts=0, status='offline' WHERE address=?`, address)
	if err != nil {
		return fmt.Errorf("wallet: 停用礦機: %w", err)
	}
	return nil
}

// ActiveMiners 回傳已開啟伺服器端排程的礦機地址（節點循環心跳用）。
func (s *Store) ActiveMiners() ([]string, error) {
	rows, err := s.db.Query(`SELECT address FROM miners WHERE active=1`)
	if err != nil {
		return nil, fmt.Errorf("wallet: 查詢啟用礦機: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("wallet: 讀取啟用礦機: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Miners 回傳全部礦機（含離線）。
func (s *Store) Miners() ([]Miner, error) {
	rows, err := s.db.Query(`SELECT address, vcpu, vgpu, hashrate, status, last_heartbeat_ts, registered_ts, active FROM miners ORDER BY hashrate DESC`)
	if err != nil {
		return nil, fmt.Errorf("wallet: 查詢礦機: %w", err)
	}
	defer rows.Close()
	out := []Miner{}
	now := time.Now().Unix()
	for rows.Next() {
		var m Miner
		if err := rows.Scan(&m.Address, &m.VCPU, &m.VGPU, &m.Hashrate, &m.Status, &m.LastHeartbeatTs, &m.RegisteredTs, &m.Active); err != nil {
			return nil, fmt.Errorf("wallet: 讀取礦機: %w", err)
		}
		// M32：在線判定統一窗口（與瓜分一致），不再用 DB status（永在線 bug）。
		m.Online = m.Status == "online" && m.LastHeartbeatTs >= now-MinerOnlineWindow
		out = append(out, m)
	}
	return out, rows.Err()
}

// OnlineMinerSplits 回傳「窗口內在線」礦工的算力瓜分份額（分子 = 各礦工算力）。
// now 由呼叫方傳入，保證同一高度下全體一致。
func (s *Store) OnlineMinerSplits(now int64) ([]MinerSplit, error) {
	// M36：分潤僅限「開機（active=1）且在心跳窗口內」的礦工——節點自身預設礦機（active=0）不參與，
	// 訪客/會員「關機」後即停發收益（不再受節點出塊心跳影響而持續進帳）。
	rows, err := s.db.Query(`SELECT address, hashrate FROM miners
WHERE active=1 AND status='online' AND last_heartbeat_ts >= ? ORDER BY hashrate DESC`, now-MinerOnlineWindow)
	if err != nil {
		return nil, fmt.Errorf("wallet: 查詢在線礦工: %w", err)
	}
	defer rows.Close()
	out := []MinerSplit{}
	for rows.Next() {
		var addr, hr string
		if err := rows.Scan(&addr, &hr); err != nil {
			return nil, fmt.Errorf("wallet: 讀取在線礦工: %w", err)
		}
		v, ok := new(big.Int).SetString(hr, 10)
		if !ok || v.Sign() <= 0 {
			continue
		}
		out = append(out, MinerSplit{Address: addr, Share: v})
	}
	return out, rows.Err()
}

// DistributeByHashrate 把 amount 依 shares 按占比分配，回傳 (地址→份額)。
// 無在線礦工時回傳 nil（呼叫方自行決定 fallback）。
func DistributeByHashrate(amount *big.Int, splits []MinerSplit) (map[string]*big.Int, error) {
	total := big.NewInt(0)
	for _, sp := range splits {
		total.Add(total, sp.Share)
	}
	if total.Sign() <= 0 {
		return nil, nil
	}
	out := make(map[string]*big.Int, len(splits))
	acc := big.NewInt(0)
	for i, sp := range splits {
		// portion = amount × share / total（末位補齊：最後一名拿剩餘，避免捨入虧損）。
		if i == len(splits)-1 {
			out[sp.Address] = new(big.Int).Sub(amount, acc)
			continue
		}
		p := new(big.Int).Mul(amount, sp.Share)
		p.Div(p, total)
		acc.Add(acc, p)
		out[sp.Address] = p
	}
	return out, nil
}
