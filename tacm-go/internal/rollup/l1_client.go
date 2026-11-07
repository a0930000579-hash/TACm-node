package rollup

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tacm/internal/crypto"
)

// L1Proof 為提交到 Layer1 的 Rollup 狀態證明（狀態承諾）。
type L1Proof struct {
	L2Height          int64  `json:"l2_height"`
	BatchIndex        int64  `json:"batch_index"`
	TxCount           int    `json:"tx_count"`
	StateRoot         string `json:"state_root"`
	PrevStateRoot     string `json:"prev_state_root"`
	Proposer          string `json:"proposer"`
	Timestamp         int64  `json:"timestamp"`
	ChallengeDeadline int64  `json:"challenge_deadline"`
}

// L1Submitter 抽象「把 L2 狀態根提交到 Layer1」。
type L1Submitter interface {
	SubmitStateRoot(proof L1Proof) (l1TxHash string, err error)
}

// parseSignerKey 由 WIF 或 64hex 私鑰構造密鑰對。
func parseSignerKey(s string) (*crypto.KeyPair, error) {
	s = strings.TrimSpace(s)
	if raw := strings.TrimPrefix(s, "0x"); len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil {
			return crypto.KeyPairFromPrivateKey(b)
		}
	}
	return crypto.KeyPairFromWIF(s)
}

// HTTPL1Submitter 經 L1 節點 RPC 提交狀態根：發起一筆真實簽名交易，
// memo 以 "l2rollup:" 前綴承載狀態證明 JSON。
type HTTPL1Submitter struct {
	nodeURL      string
	contractAddr string
	proposerKP   *crypto.KeyPair
	client       *http.Client
}

// NewHTTPL1Submitter 構造 L1 提交客戶端。
func NewHTTPL1Submitter(nodeURL, contractAddr, proposerWIF string) (*HTTPL1Submitter, error) {
	kp, err := parseSignerKey(proposerWIF)
	if err != nil {
		return nil, fmt.Errorf("rollup: L1 提議者密鑰錯誤: %w", err)
	}
	if contractAddr == "" {
		return nil, fmt.Errorf("rollup: 缺少 L1 Rollup 合約地址")
	}
	return &HTTPL1Submitter{
		nodeURL:      strings.TrimRight(nodeURL, "/"),
		contractAddr: contractAddr,
		proposerKP:   kp,
		client:       &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// SubmitStateRoot 構造並提交 L1 狀態根交易，返回 L1 交易哈希。
func (s *HTTPL1Submitter) SubmitStateRoot(proof L1Proof) (string, error) {
	addr, err := s.proposerKP.Address()
	if err != nil {
		return "", err
	}
	nonce, err := s.fetchNonce(addr)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	tx := map[string]any{
		"from": addr, "to": s.contractAddr,
		"amount": "0", "fee": "0.001",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(s.proposerKP.PublicKeyCompressed()),
		"memo":   "l2rollup:" + string(payload),
	}
	sig, err := crypto.SignTransaction(tx, s.proposerKP.PrivateKey())
	if err != nil {
		return "", err
	}
	tx["signature"] = sig

	body, err := json.Marshal(tx)
	if err != nil {
		return "", err
	}
	res, err := s.client.Post(s.nodeURL+"/tx/submit",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("rollup: 提交 L1 失敗: %w", err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("rollup: L1 拒絕(HTTP %d): %s", res.StatusCode, raw)
	}
	h, _ := out["tx_hash"].(string)
	if h == "" {
		return "", fmt.Errorf("rollup: L1 未返回 tx_hash: %s", raw)
	}
	return h, nil
}

func (s *HTTPL1Submitter) fetchNonce(addr string) (int64, error) {
	res, err := s.client.Get(s.nodeURL + "/account/" + addr)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	var acc struct {
		Nonce int64 `json:"nonce"`
	}
	if err := json.NewDecoder(res.Body).Decode(&acc); err != nil {
		return 0, err
	}
	return acc.Nonce, nil
}
