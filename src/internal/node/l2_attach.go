package node

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"tacm/internal/crypto"
	"tacm/internal/rollup"
	"tacm/internal/vm"
)

// L2ContractAddress 返回 L1 上代表 Rollup 的系統合約地址（確定性、合法 tx0）。
func L2ContractAddress() string {
	h := vm.Keccak256([]byte("tac:l2:rollup-contract"))
	return crypto.Hash160ToAddress(h[:20])
}

// l1InProcess 以本節點密鑰直接在 L1（chaindb）提交狀態根交易，無 HTTP 自調用。
type l1InProcess struct {
	n        *Node
	contract string
}

// SubmitStateRoot 構造一筆真實簽名 L1 交易（memo 承載證明）並提交。
func (s *l1InProcess) SubmitStateRoot(p rollup.L1Proof) (string, error) {
	kp := s.n.keypair
	addr := s.n.nodeAddress
	nonce := s.n.db.GetNonce(addr)

	payload, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("node: 序列化 L2 證明失敗: %w", err)
	}
	tx := map[string]any{
		"from": addr, "to": s.contract,
		"amount": "0", "fee": "0.001",
		"nonce": nonce, "ts": time.Now().Unix(),
		"pubkey": hex.EncodeToString(kp.PublicKeyCompressed()),
		"memo":   "l2rollup:" + string(payload),
	}
	sig, err := crypto.SignTransaction(tx, kp.PrivateKey())
	if err != nil {
		return "", err
	}
	tx["signature"] = sig
	return s.n.SubmitTransaction(tx)
}

// AttachL2 建立並接入 Layer2 Rollup（含後台自動批量/提交服務）。
func (n *Node) AttachL2() error {
	l2cfg := rollup.DefaultConfig(n.cfg.DataDir + "/l2")
	l2cfg.L1ContractAddress = L2ContractAddress()

	r, err := rollup.New(l2cfg)
	if err != nil {
		return err
	}
	r.SetL1(&l1InProcess{n: n, contract: L2ContractAddress()})
	n.l2 = r
	n.l2svc = rollup.StartService(r)
	return nil
}

// L2 返回接入的 Rollup（未啟用為 nil）。
func (n *Node) L2() *rollup.Rollup { return n.l2 }
