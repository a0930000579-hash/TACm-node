package p2p

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/btcsuite/btcd/btcec/v2"
)

// ErrPeerAuth 表示對端以 400 拒絕加密握手（金鑰/協議層失效）。
// 觸發本端清除快取公鑰並重新取得後重試一次。
var ErrPeerAuth = errors.New("p2p: peer rejected auth (400)")

// invalidatePeerCrypto 清除對端快取的公鑰與通道狀態。對端重啟後其 ECDH
// 金鑰輪替，舊公鑰快取會讓加密握手永久失敗；清除後下次 sealBody 重新向
// /p2p/pubkey 取得新公鑰，SharedKey 依新公鑰產生新共享密鑰（舊密鑰條目無害保留）。
func (n *Network) invalidatePeerCrypto(origin string) {
	n.peerPubKeys.Delete(origin)
	n.peerCipher.Delete(origin)
}

// Secure 提供 P2P 訊息層加密：secp256k1 ECDH 交換共享密鑰，
// AES-256-GCM 加密每則 JSON body（與主流鏈 devp2p 的加密思想對齊，
// 但以 HTTP/JSON 協議的最小侵入方式落地）。
type Secure struct {
	priv *btcec.PrivateKey
	// peerKeys 緩存 peer pubkey hex → 共享密鑰（ECDH 結果）。
	peerKeys sync.Map // map[string][32]byte
}

// NewSecure 生成節點 P2P 加密金鑰對（每次啟動隨機，非持久化）。
func NewSecure() (*Secure, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("p2p: 生成加密金鑰失敗: %w", err)
	}
	return &Secure{priv: priv}, nil
}

// PubKeyHex 回本節點壓縮公鑰 hex（握手時提供給對端）。
func (s *Secure) PubKeyHex() string {
	return hex.EncodeToString(s.priv.PubKey().SerializeCompressed())
}

// SharedKey 以對端公鑰計算 ECDH 共享密鑰（SHA256(壓縮共享點)），並快取。
func (s *Secure) SharedKey(peerPubHex string) ([32]byte, error) {
	var zero [32]byte
	if cached, ok := s.peerKeys.Load(peerPubHex); ok {
		return cached.([32]byte), nil
	}
	pubBytes, err := hex.DecodeString(peerPubHex)
	if err != nil {
		return zero, fmt.Errorf("p2p: peer pubkey hex 非法: %w", err)
	}
	pub, err := btcec.ParsePubKey(pubBytes)
	if err != nil {
		return zero, fmt.Errorf("p2p: peer pubkey 解析失敗: %w", err)
	}
	shared := btcec.GenerateSharedSecret(s.priv, pub) // 33B 壓縮共享點
	key := sha256Sum(shared)
	s.peerKeys.Store(peerPubHex, key)
	return key, nil
}

// Encrypt 以共享密鑰 AES-256-GCM 加密：輸出「nonce(12B) || ciphertext+tag」。
func EncryptPayload(key [32]byte, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("p2p: AES 初始化失敗: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("p2p: GCM 初始化失敗: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("p2p: nonce 生成失敗: %w", err)
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// DecryptPayload 解開 EncryptPayload 的輸出。
func DecryptPayload(key [32]byte, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("p2p: AES 初始化失敗: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("p2p: GCM 初始化失敗: %w", err)
	}
	if len(data) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("p2p: 密文過短")
	}
	nonce, ct := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("p2p: 解密失敗（金鑰不符或資料被竄改）: %w", err)
	}
	return plain, nil
}

func sha256Sum(b []byte) [32]byte {
	h := sha256.New()
	h.Write(b)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ---- HTTP 訊息層包裝：加密 body 格式 {"pub","iv","data"} ----

type encEnvelope struct {
	Pub  string `json:"pub"`  // 發送端壓縮公鑰 hex
	IV   string `json:"iv"`   // AES-GCM nonce hex
	Data string `json:"data"` // ciphertext+tag hex
}

// peerOrigin 剝離 URL 到 origin（http://host[:port]），供 pubkey 快取與握手使用。
func peerOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Scheme + "://" + u.Host
}

// fetchPeerPubKey 向 peer 索取加密公鑰並快取（GET /p2p/pubkey）。
func (n *Network) fetchPeerPubKey(baseURL string) (string, error) {
	if n.Secure == nil {
		return "", errors.New("p2p: 加密未啟用")
	}
	if v, ok := n.peerPubKeys.Load(baseURL); ok {
		return v.(string), nil
	}
	req, err := http.NewRequestWithContext(n.ctx, http.MethodGet, baseURL+"/p2p/pubkey", nil)
	if err != nil {
		return "", err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("p2p: 索取對端公鑰失敗: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		PubKey string `json:"pubkey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("p2p: 對端公鑰回應解析失敗: %w", err)
	}
	if out.PubKey == "" {
		return "", errors.New("p2p: 對端未啟用加密")
	}
	n.peerPubKeys.Store(baseURL, out.PubKey)
	return out.PubKey, nil
}

// setPeerCipher 記錄與指定 peer 的通道加密狀態（供 Peer.Encrypted 展示）。
func (n *Network) setPeerCipher(baseURL string, enc bool) { n.peerCipher.Store(baseURL, enc) }

// peerEncrypted 返回與指定 peer 的通道是否加密；未知時預設 true（本端加密開啟）。
func (n *Network) peerEncrypted(baseURL string) bool {
	if v, ok := n.peerCipher.Load(baseURL); ok {
		return v.(bool)
	}
	return n.cfg.Encrypt && n.Secure != nil
}

// sealBody 加密 HTTP body（Encrypt 開啟時）：向對端索取公鑰 → ECDH → AES-GCM。
// v65：對端不支援加密（舊版節點）或索取失敗時**自動降級明文**並記錄通道狀態，
// 避免加密節點與舊節點混跑時連線中斷；降級狀態經 Peer.Encrypted 可視化。
func (n *Network) sealBody(rawURL string, plain []byte) ([]byte, error) {
	if !n.cfg.Encrypt || n.Secure == nil {
		return plain, nil
	}
	origin := peerOrigin(rawURL)
	// rawURL 可能是完整端點（.../p2p/hello、.../p2p/gossip），pubkey 握手固定走 origin。
	peerPub, err := n.fetchPeerPubKey(origin)
	if err != nil {
		// 對端不支援加密（/p2p/pubkey 404/400/空）或暫時不可達：
		// 降級明文保證網絡不斷裂，並標記通道狀態供 UI 警示。
		n.setPeerCipher(origin, false)
		return plain, nil
	}
	key, err := n.Secure.SharedKey(peerPub)
	if err != nil {
		n.setPeerCipher(origin, false)
		return plain, nil
	}
	n.setPeerCipher(origin, true)
	sealed, err := EncryptPayload(key, plain)
	if err != nil {
		return nil, err
	}
	nonce, ct := sealed[:12], sealed[12:]
	env := encEnvelope{
		Pub:  n.Secure.PubKeyHex(),
		IV:   hex.EncodeToString(nonce),
		Data: hex.EncodeToString(ct),
	}
	return json.Marshal(env)
}

// openBody 解密 HTTP body（Encrypt 開啟時）；返回原 JSON bytes。
// v65 相容：對端若為舊版明文節點（body 不是加密 envelope），直接視明文
// 處理，避免加密端拒絕明文節點的 hello/gossip（全網可混跑）。
func (n *Network) openBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("p2p: 讀取 body 失敗: %w", err)
	}
	if !n.cfg.Encrypt || n.Secure == nil {
		return body, nil
	}
	var env encEnvelope
	if err := json.Unmarshal(body, &env); err != nil || env.Pub == "" || env.IV == "" || env.Data == "" {
		// 非加密 envelope（明文節點發來或格式異常）：按明文相容處理。
		return body, nil
	}
	key, err := n.Secure.SharedKey(env.Pub)
	if err != nil {
		return nil, err
	}
	iv, err := hex.DecodeString(env.IV)
	if err != nil {
		return nil, fmt.Errorf("p2p: iv hex 非法: %w", err)
	}
	data, err := hex.DecodeString(env.Data)
	if err != nil {
		return nil, fmt.Errorf("p2p: data hex 非法: %w", err)
	}
	sealed := append(iv, data...)
	return DecryptPayload(key, sealed)
}

// handlePubKey 回本節點加密公鑰（加密握手第一步）。
func (n *Network) handlePubKey(w http.ResponseWriter, r *http.Request) {
	if n.Secure == nil {
		writeP2PErr(w, http.StatusBadRequest, "encryption not enabled")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"pubkey":"` + n.Secure.PubKeyHex() + `"}`))
}
