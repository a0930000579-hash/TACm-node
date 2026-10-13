package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"tacm/internal/crypto"
)

// FaucetClaim 為水龍頭單日配額上限（每日每地址可領取額度，單位 TACm）。
const FaucetClaimAmount = "1000"

// SendFromNode 以節點金鑰向目標地址發送普通轉帳（Faucet 水龍頭用）。
// 回傳交易哈希與簽名者地址。
func (n *Node) SendFromNode(to, amount string) (string, string, error) {
	if !crypto.IsValidAddress(to) {
		return "", "", errors.New("invalid recipient address")
	}
	kp := n.keypair
	if kp == nil {
		return "", "", errors.New("node key unavailable")
	}
	from, err := kp.Address()
	if err != nil {
		return "", "", err
	}
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	tx := map[string]any{
		"from": from, "to": to,
		"amount": amount, "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		return "", "", fmt.Errorf("faucet: sign: %w", err)
	}
	tx["signature"] = sig
	txHash, err := n.SubmitTransaction(tx)
	if err != nil {
		return "", "", fmt.Errorf("faucet: submit: %w", err)
	}
	return txHash, from, nil
}
