package dapp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"tacm/internal/crypto"

	"github.com/btcsuite/btcd/btcec/v2"
)

// 本檔實作 DApp 寫操作的防冒名簽名驗證（EIP-191 personal_sign）：
//   - 用戶端以已連結錢包對 canonical 訊息簽名；
//   - 伺服器由簽名 recover 出公鑰，派生 tx0 / 0x 兩種地址，與聲稱地址比對；
//   - nonce 一次性（記憶體、TTL）＋ timestamp 視窗防重放。
//
// 驗證邏輯放在本包，路由層只負責收送 JSON。

// 寫操作可簽名的動作。
const (
	DappActionRegister   = "register"
	DappActionRate       = "rate"
	DappActionFavorite   = "favorite"
	DappActionAdmin      = "admin"
	DappActionUnfavorite = "favorite" // 收藏/取消共用同一動作綁定 dapp_id
)

// 簽名時間視窗：容許時鐘誤差與用戶猶豫。
const (
	dappSigMaxFutureS = 300 // +5 分鐘
	dappSigMaxPastS   = 600 // -10 分鐘
	dappNonceTTL      = 600 // nonce 有效 10 分鐘
)

// dappSignDomain 簽名網域前綴（避免與登入 / 其他訊息混用）。
const dappSignDomain = "TAC DApp Store Authorization"

// DAppSignMessage 重建簽名訊息（用戶端必須產生完全相同字串）。
// payload 綁定動作內容：register=name|url、rate=dapp|score、favorite=dapp、admin=dapp|status。
func DAppSignMessage(action, address, nonce string, ts int64, payload string) string {
	return fmt.Sprintf("%s\nAction: %s\nAddress: %s\nNonce: %s\nTimestamp: %d\nPayload: %s",
		dappSignDomain, action, strings.TrimSpace(address), nonce, ts, payload)
}

// dappNonceEntry 一次性 nonce。
type dappNonceEntry struct {
	address string
	action  string
	expires int64
}

// IssueNonce 核發一次性 nonce（記憶體）。回傳 (nonce, timestamp)。
// 呼叫端應在成功寫入後由 VerifyWriteSignature 消費；過期自動作廢。
func (s *Store) IssueNonce(address, action string) (string, int64, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", 0, errors.New("dapp: 缺少地址")
	}
	if !validDappAction(action) {
		return "", 0, fmt.Errorf("dapp: 未知簽名動作 %q", action)
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, fmt.Errorf("dapp: 產生 nonce: %w", err)
	}
	nonce := hex.EncodeToString(buf)
	now := time.Now().Unix()

	s.mu.Lock()
	// 順手清除過期 nonce。
	for k, e := range s.nonces {
		if now > e.expires {
			delete(s.nonces, k)
		}
	}
	s.nonces[nonce] = dappNonceEntry{address: address, action: action, expires: now + dappNonceTTL}
	s.mu.Unlock()
	return nonce, now, nil
}

func validDappAction(action string) bool {
	switch action {
	case DappActionRegister, DappActionRate, DappActionFavorite, DappActionAdmin:
		return true
	}
	return false
}

// VerifyWriteSignature 驗證寫操作簽名：
//  1. timestamp 在容許視窗內；
//  2. nonce 存在、未用、未過期、且 address/action 相符（一次性，驗證後刪除）；
//  3. EIP-191 recover 出的簽名者身份與聲稱 address 一致（tx0 或 0x 任一）。
//
// 成功回 nil，失敗回明確錯誤（路由轉 4xx）。
func (s *Store) VerifyWriteSignature(action, address, nonce string, ts int64, payload, sigHex string) error {
	address = strings.TrimSpace(address)
	nonce = strings.TrimSpace(nonce)
	if address == "" {
		return errors.New("dapp: 缺少簽名地址")
	}
	if nonce == "" {
		return errors.New("dapp: 缺少 nonce")
	}
	if !validDappAction(action) {
		return fmt.Errorf("dapp: 未知簽名動作 %q", action)
	}
	if strings.TrimSpace(sigHex) == "" {
		return errors.New("dapp: 缺少簽名（需以已連結錢包簽署；免金鑰模式無法寫入）")
	}

	now := time.Now().Unix()
	if ts < now-dappSigMaxPastS || ts > now+dappSigMaxFutureS {
		return errors.New("dapp: 簽名時間戳過期，請重新取得 nonce")
	}

	// nonce 一次性：無論成敗皆作廢。
	s.mu.Lock()
	entry, ok := s.nonces[nonce]
	if ok {
		delete(s.nonces, nonce)
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("dapp: nonce 不存在或已使用，請重新取得")
	}
	if now > entry.expires {
		return errors.New("dapp: nonce 已過期，請重新取得")
	}
	if !strings.EqualFold(entry.address, address) {
		return errors.New("dapp: nonce 與地址不符")
	}
	if entry.action != action {
		return fmt.Errorf("dapp: nonce 動作不符（需 %s）", action)
	}

	// EIP-191 解簽。
	raw := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(sigHex), "0x"), "0X")
	sig, err := hex.DecodeString(raw)
	if err != nil || len(sig) != 65 {
		return errors.New("dapp: 簽名格式需為 65-byte hex（personal_sign 輸出）")
	}
	message := DAppSignMessage(action, address, nonce, ts, payload)
	hash := crypto.HashPersonalMessage([]byte(message))
	pub, err := crypto.RecoverSignerFromSig(hash, sig)
	if err != nil {
		return fmt.Errorf("dapp: 簽名恢復失敗: %w", err)
	}
	if !matchSignerIdentity(pub, address) {
		return errors.New("dapp: 簽名者身份與聲稱地址不符（冒名已拒絕）")
	}
	return nil
}

// matchSignerIdentity 比對 recover 出的公鑰身份與聲稱地址。
// TAC 錢包地址為 tx0（Hash160(compressed)）；注入式 Web3 錢包為 0x（keccak uncompressed）。
// 兩者皆由同一 secp256k1 私鑰派生，任一相符即通過。
func matchSignerIdentity(pub *btcec.PublicKey, claimed string) bool {
	claimed = strings.TrimSpace(claimed)
	if claimed == "" {
		return false
	}
	// tx0：Hash160ToAddress(Hash160(compressedPub))。
	if tx0, err := crypto.PubKeyToAddress(pub.SerializeCompressed()); err == nil {
		if strings.EqualFold(tx0, claimed) {
			return true
		}
	}
	// 0x：keccak(uncompressed[1:]) 尾 20 bytes。
	if strings.HasPrefix(strings.ToLower(claimed), "0x") && len(claimed) == 42 {
		uncomp := pub.SerializeUncompressed()[1:]
		d := crypto.Keccak256(uncomp)
		derived := crypto.EthAddressHex(d[len(d)-20:])
		if strings.EqualFold(derived, claimed) {
			return true
		}
	}
	return false
}

// DAppAdmins 回傳目前授權審核的地址集合（key 已轉小寫）。
// 來源：環境變數 TAC_DAPP_ADMINS（逗號分隔）。未設定回空集合。
func DAppAdmins() map[string]struct{} {
	out := map[string]struct{}{}
	raw := strings.TrimSpace(os.Getenv("TAC_DAPP_ADMINS"))
	if raw == "" {
		return out
	}
	for _, a := range strings.Split(raw, ",") {
		a = strings.TrimSpace(a)
		if a != "" {
			out[strings.ToLower(a)] = struct{}{}
		}
	}
	return out
}

// IsDAppAdmin 判斷地址是否在審核白名單（tx0 / 0x 皆可，大小寫不敏感）。
// 管理員需以與其簽名錢包一致的地址格式列入 TAC_DAPP_ADMINS。
func IsDAppAdmin(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return false
	}
	admins := DAppAdmins()
	_, ok := admins[strings.ToLower(address)]
	return ok
}
