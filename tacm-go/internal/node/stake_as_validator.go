package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
)

// StakeAsValidator M149m：節點以自己的金鑰發鏈上質押交易（from=節點地址、to=質押池）。
// 交易入塊後由全網重放累計驗證人權重，動態驗證人集（每 60 秒）重建時入會。
// 返回交易哈希。
func (n *Node) StakeAsValidator(amount float64) (string, error) {
	if n.keypair == nil {
		return "", errors.New("node: 節點金鑰不可用")
	}
	if amount <= 0 {
		return "", errors.New("node: 質押金額必須為正")
	}
	from, err := n.keypair.Address()
	if err != nil {
		return "", err
	}
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	tx := map[string]any{
		"from": from, "to": chaindb.PosStakePoolAddr,
		"amount": strconv.FormatFloat(amount, 'f', 8, 64), "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(n.keypair.PublicKeyCompressed()),
		"memo":   chaindb.PosStakeMemo,
	}
	sig, err := crypto.SignTransaction(tx, n.keypair.PrivateKey())
	if err != nil {
		return "", fmt.Errorf("node: 簽名質押交易: %w", err)
	}
	tx["signature"] = sig
	return n.SubmitTransaction(tx)
}

// RegisterAsPotNode M149m-9：PoT 節點公鑰註冊——節點免費上鏈註冊公鑰
// （amount=0、fee=0、memo=node:reg），註冊後依在線時長（時間權證）取得
// 無質押出塊資格；任何節點同步同鏈即得到相同的 PoT 節點集。
func (n *Node) RegisterAsPotNode() (string, error) {
	if n.keypair == nil {
		return "", errors.New("node: 節點金鑰不可用")
	}
	from, err := n.keypair.Address()
	if err != nil {
		return "", err
	}
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	tx := map[string]any{
		"from": from, "to": from,
		"amount": "0", "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(n.keypair.PublicKeyCompressed()),
		"memo":   chaindb.NodeRegMemo,
	}
	sig, err := crypto.SignTransaction(tx, n.keypair.PrivateKey())
	if err != nil {
		return "", fmt.Errorf("node: 簽名 PoT 節點註冊交易: %w", err)
	}
	tx["signature"] = sig
	return n.SubmitTransaction(tx)
}

// StakeAsBootstrap M149m-2：新節點首次「創節點」——發引導獎勵交易（memo=pos:bootstrap），
// 鏈上核發 1,000 TACm 至節點錢包並自動質押，直接具備驗證人資格（限額 50 名、每地址一次）。
func (n *Node) StakeAsBootstrap() (string, error) {
	if n.keypair == nil {
		return "", errors.New("node: 節點金鑰不可用")
	}
	from, err := n.keypair.Address()
	if err != nil {
		return "", err
	}
	nonce := n.db.GetNonce(from) + n.pendingTxCount(from)
	tx := map[string]any{
		"from": from, "to": chaindb.PosStakePoolAddr,
		"amount": "0", "fee": "0",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(n.keypair.PublicKeyCompressed()),
		"memo":   chaindb.PosBootstrapMemo,
	}
	sig, err := crypto.SignTransaction(tx, n.keypair.PrivateKey())
	if err != nil {
		return "", fmt.Errorf("node: 簽名引導獎勵交易: %w", err)
	}
	tx["signature"] = sig
	return n.SubmitTransaction(tx)
}
