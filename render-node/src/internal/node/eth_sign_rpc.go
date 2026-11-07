package node

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tacm/internal/crypto"
)

// 錢包簽名 RPC（主流鏈 dApp/交易所生態標準）：
//   - personal_sign          [address, message] → EIP-191 簽名（0x…65 bytes）
//   - eth_sign               [address, message] → keccak256(message) 簽名（legacy）
//   - eth_signTypedData_v4   [address, typedDataJSON] → EIP-712 簽名（0x…65 bytes）
//   - eth_ecRecover          [message, sig] → 簽名者地址（0x…40）
//   - personal_ecRecover     [message, sig] → 簽名者地址（EIP-191 語義）
//
// 節點只有一個金鑰：address 參數必須對應節點帳戶（與 geth 解鎖帳戶語義一致）。

// signerForAccount 驗證 address 對應節點金鑰，回私鑰與節點 eth 地址。
func (s *RPCServer) signerForAccount(addrParam string) ([]byte, string, error) {
	got := strings.ToLower(strip0x(addrParam))
	if len(s.importedKeys) > 0 {
		s.importedMu.Lock()
		priv, ok := s.importedKeys[got]
		s.importedMu.Unlock()
		if ok {
			return priv, got, nil
		}
	}
	kp := s.node.keypair
	if kp == nil {
		return nil, "", errors.New("node key unavailable")
	}
	nodeAddr, err := kp.EthAddress()
	if err != nil {
		return nil, "", err
	}
	want := strings.ToLower(strip0x(nodeAddr))
	if want != got {
		return nil, "", fmt.Errorf("account not found or locked: %s", addrParam)
	}
	return kp.PrivateKey(), nodeAddr, nil
}

// personalImportRawKey [hexKey] → 匯入私鑰（僅記憶體快取，不落盤）並回傳 eth 地址。
// 與 geth personal_importRawKey 相容：64 hex（可帶 0x），passphrase 忽略。
func (s *RPCServer) personalImportRawKey(params []any) (string, error) {
	if len(params) < 1 {
		return "", errors.New("personal_importRawKey 需要 [hexKey]")
	}
	hexKey := strip0x(paramString(params, 0))
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid private key hex (expect 64 hex chars)")
	}
	kp, err := crypto.KeyPairFromPrivateKey(raw)
	if err != nil {
		return "", errors.New("invalid private key")
	}
	addr, err := kp.EthAddress()
	if err != nil {
		return "", err
	}
	key := strings.ToLower(strip0x(addr))
	s.importedMu.Lock()
	if s.importedKeys == nil {
		s.importedKeys = make(map[string][]byte)
	}
	s.importedKeys[key] = append([]byte(nil), raw...)
	s.importedMu.Unlock()
	return addr, nil
}

// debugGetBadBlocks [] → 本鏈無 bad-block 追蹤（finality 拒絕壞塊），回空陣列。
func (s *RPCServer) debugGetBadBlocks(params []any) (any, error) {
	return []any{}, nil
}

// ethPersonalSign [address, message]。
func (s *RPCServer) ethPersonalSign(params []any) (string, error) {
	if len(params) < 2 {
		return "", errors.New("personal_sign 需要 [address, message]")
	}
	msgHex := strip0x(paramString(params, 1))
	msg, err := hex.DecodeString(msgHex)
	if err != nil {
		return "", errors.New("invalid message hex")
	}
	priv, _, err := s.signerForAccount(paramString(params, 0))
	if err != nil {
		return "", err
	}
	sig, err := crypto.SignPersonalMessage(msg, priv)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// ethSign [address, message]（legacy：直接 keccak256(message)）。
func (s *RPCServer) ethSign(params []any) (string, error) {
	if len(params) < 2 {
		return "", errors.New("eth_sign 需要 [address, message]")
	}
	msgHex := strip0x(paramString(params, 1))
	msg, err := hex.DecodeString(msgHex)
	if err != nil {
		return "", errors.New("invalid message hex")
	}
	priv, _, err := s.signerForAccount(paramString(params, 0))
	if err != nil {
		return "", err
	}
	sig, err := crypto.SignEthereumMessage(crypto.Keccak256(msg), priv)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// ethSignTypedDataV4 [address, typedDataJSON]。
func (s *RPCServer) ethSignTypedDataV4(params []any) (string, error) {
	if len(params) < 2 {
		return "", errors.New("eth_signTypedData_v4 需要 [address, typedDataJSON]")
	}
	typedJSON := []byte(paramString(params, 1))
	var probe map[string]any
	if err := json.Unmarshal(typedJSON, &probe); err != nil {
		return "", errors.New("invalid typed data JSON")
	}
	hash, err := crypto.HashTypedDataV4(typedJSON)
	if err != nil {
		return "", err
	}
	priv, _, err := s.signerForAccount(paramString(params, 0))
	if err != nil {
		return "", err
	}
	sig, err := crypto.SignEthereumMessage(hash, priv)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// ethEcRecover [message, sig]：回簽名者地址（keccak256(message) 語義）。
func (s *RPCServer) ethEcRecover(params []any) (string, error) {
	msgHex := strip0x(paramString(params, 0))
	sigHex := strip0x(paramString(params, 1))
	msg, err := hex.DecodeString(msgHex)
	if err != nil {
		return "", errors.New("invalid message hex")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", errors.New("invalid signature hex")
	}
	return crypto.RecoverSignerAddressFromSig(crypto.Keccak256(msg), sig)
}

// personalEcRecover [message, sig]：回簽名者地址（EIP-191 語義）。
func (s *RPCServer) personalEcRecover(params []any) (string, error) {
	msgHex := strip0x(paramString(params, 0))
	sigHex := strip0x(paramString(params, 1))
	msg, err := hex.DecodeString(msgHex)
	if err != nil {
		return "", errors.New("invalid message hex")
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", errors.New("invalid signature hex")
	}
	return crypto.RecoverSignerAddressFromSig(crypto.HashPersonalMessage(msg), sig)
}
