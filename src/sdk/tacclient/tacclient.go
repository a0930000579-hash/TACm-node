// Package tacclient 是 TAC Ai 智能鏈的官方開發者 SDK：生成/導入密鑰、
// 簽署交易、提交與查詢節點 RPC。第三方 dApp 開發者可直接在本 module 內
// 使用（或連同 internal/crypto 複製到自己的 Go 專案）。
package tacclient

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tacm/internal/crypto"
)

// ---- 資料模型（對應節點 RPC JSON，欄位與 internal/chaindb 一致） ----

// Status 為節點對外狀態（GET /status）。
type Status struct {
	NodeID         string  `json:"node_id"`
	Address        string  `json:"address"`
	Network        string  `json:"network"`
	ChainID        string  `json:"chain_id"`
	BlockHeight    int64   `json:"block_height"`
	FinalHeight    int64   `json:"final_block_height"`
	MempoolSize    int64   `json:"mempool_size"`
	Difficulty     int     `json:"effective_difficulty"`
	UptimeSec      int64   `json:"uptime_sec"`
	Consensus      string  `json:"consensus"`
	EmissionModel  string  `json:"emission_model"`
	MaxSupply      float64 `json:"max_supply"`
	EmissionYears  int     `json:"emission_years"`
	AnnualDecayPct float64 `json:"annual_decay_pct"`
}

// Block 為區塊頭（GET /block/{height}）。
type Block struct {
	Height          int64   `json:"height"`
	Hash            string  `json:"hash"`
	PrevHash        *string `json:"prev_hash"`
	MerkleRoot      string  `json:"merkle_root"`
	Proposer        string  `json:"proposer"`
	ProposerAddress string  `json:"proposer_address,omitempty"`
	Ts              int64   `json:"ts"`
	TxCount         int     `json:"tx_count"`
	Difficulty      int     `json:"difficulty"`
	Nonce           int64   `json:"nonce"`
	Size            int     `json:"size"`
}

// Transaction 為已打包交易（GET /tx/{hash}）。
type Transaction struct {
	TxHash      string `json:"tx_hash"`
	BlockHeight int64  `json:"block_height"`
	BlockHash   string `json:"block_hash"`
	TxIndex     int    `json:"tx_index"`
	FromAddr    string `json:"from"`
	ToAddr      string `json:"to"`
	Amount      string `json:"amount"`
	Fee         string `json:"fee"`
	Nonce       int64  `json:"nonce"`
	Ts          int64  `json:"ts"`
	Signature   string `json:"signature"`
	Pubkey      string `json:"pubkey"`
	Memo        string `json:"memo"`
	Status      string `json:"status"`
}

// Account 為地址帳戶狀態（GET /account/{address}）。
type Account struct {
	Address    string `json:"address"`
	Balance    string `json:"balance"`
	Nonce      int64  `json:"nonce"`
	Pubkey     string `json:"pubkey"`
	FirstSeen  int64  `json:"first_seen"`
	LastActive int64  `json:"last_active"`
}

// ---- 密鑰 ----

// Key 封裝 secp256k1 密鑰對與 tx0 地址。
type Key struct {
	kp   *crypto.KeyPair
	addr string
}

// GenerateKey 生成新的密鑰對。
func GenerateKey() (*Key, error) {
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	addr, err := kp.Address()
	if err != nil {
		return nil, err
	}
	return &Key{kp: kp, addr: addr}, nil
}

// KeyFromPrivateKeyHex 從 32 字節十六進制私鑰導入密鑰對。
func KeyFromPrivateKeyHex(hexKey string) (*Key, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(hexKey), "0x"))
	if err != nil {
		return nil, fmt.Errorf("tacclient: 私鑰非合法 hex: %w", err)
	}
	kp, err := crypto.KeyPairFromPrivateKey(b)
	if err != nil {
		return nil, err
	}
	addr, err := kp.Address()
	if err != nil {
		return nil, err
	}
	return &Key{kp: kp, addr: addr}, nil
}

// Address 返回 tx0 開頭地址。
func (k *Key) Address() string { return k.addr }

// PrivateKeyHex 返回 32 字節私鑰的 hex 表示（請妥善保管）。
func (k *Key) PrivateKeyHex() string {
	return hex.EncodeToString(k.kp.PrivateKey())
}

// PublicKeyHex 返回壓縮公鑰 hex（提交交易時作為 pubkey 欄位）。
func (k *Key) PublicKeyHex() string {
	return hex.EncodeToString(k.kp.PublicKeyCompressed())
}

// ---- 節點客戶端 ----

// Client 是 TAC 節點 HTTP RPC 客戶端。
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient 建立節點客戶端；baseURL 如 http://127.0.0.1:8080。
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: 15 * time.Second},
	}
}

// do 執行 HTTP 請求並解析 JSON 至 out（out 為 nil 時僅檢查狀態碼）。
func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("tacclient: 序列化請求失敗: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("tacclient: 建立請求失敗: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("tacclient: 節點不可達: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("tacclient: 讀取響應失敗: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("tacclient: 節點錯誤 %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("tacclient: 解析響應失敗: %w", err)
		}
	}
	return nil
}

// Status 查詢節點狀態（GET /status）。
func (c *Client) Status() (*Status, error) {
	var st Status
	if err := c.do(http.MethodGet, "/status", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// LatestHeight 返回節點鏈頂高度。
func (c *Client) LatestHeight() (int64, error) {
	st, err := c.Status()
	if err != nil {
		return 0, err
	}
	return st.BlockHeight, nil
}

// Account 查詢地址帳戶（不存在時回零帳戶）。
func (c *Client) Account(addr string) (*Account, error) {
	var acc Account
	if err := c.do(http.MethodGet, "/account/"+addr, nil, &acc); err != nil {
		return nil, err
	}
	return &acc, nil
}

// Balance 返回地址餘額（字符串，可能含小數）。
func (c *Client) Balance(addr string) (string, error) {
	acc, err := c.Account(addr)
	if err != nil {
		return "", err
	}
	return acc.Balance, nil
}

// Nonce 返回地址下一個可用交易序號（防重放）。
func (c *Client) Nonce(addr string) (int64, error) {
	acc, err := c.Account(addr)
	if err != nil {
		return 0, err
	}
	return acc.Nonce, nil
}

// Block 查詢指定高度區塊。
func (c *Client) Block(height int64) (*Block, error) {
	var b Block
	if err := c.do(http.MethodGet, "/block/"+strconv.FormatInt(height, 10), nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// Transaction 按哈希查詢已打包交易。
func (c *Client) Transaction(hash string) (*Transaction, error) {
	var tx Transaction
	if err := c.do(http.MethodGet, "/tx/"+hash, nil, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

// Mempool 返回內存池交易（未打包）。
func (c *Client) Mempool() ([]map[string]any, error) {
	var txs []map[string]any
	if err := c.do(http.MethodGet, "/mempool", nil, &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// Submit 提交已簽署交易 map（需含 from/to/amount/fee/nonce/ts/signature/pubkey）。
// 成功返回交易哈希。
func (c *Client) Submit(tx map[string]any) (string, error) {
	var res struct {
		OK     bool   `json:"ok"`
		TxHash string `json:"tx_hash"`
	}
	if err := c.do(http.MethodPost, "/tx/submit", tx, &res); err != nil {
		return "", err
	}
	if !res.OK || res.TxHash == "" {
		return "", errors.New("tacclient: 節點未確認交易")
	}
	return res.TxHash, nil
}

// Transfer 建構並簽署一筆轉帳交易後提交；fee 為手續費，memo 可為空。
// 返回交易哈希（交易先入內存池，出塊後可查 Transaction）。
func (c *Client) Transfer(key *Key, to, amount, fee, memo string) (string, error) {
	nonce, err := c.Nonce(key.Address())
	if err != nil {
		return "", fmt.Errorf("tacclient: 查詢 nonce 失敗: %w", err)
	}
	tx := map[string]any{
		"from": key.Address(), "to": to,
		"amount": amount, "fee": fee,
		"nonce": nonce, "ts": time.Now().Unix(),
		"memo": memo, "pubkey": key.PublicKeyHex(),
	}
	sig, err := crypto.SignTransaction(tx, key.kp.PrivateKey())
	if err != nil {
		return "", fmt.Errorf("tacclient: 簽署交易失敗: %w", err)
	}
	tx["signature"] = sig
	return c.Submit(tx)
}
