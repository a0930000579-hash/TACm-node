package bridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"tacm/internal/crypto"
)

// ExecResult 為守衛聚合達標後的執行結果。
type ExecResult struct {
	Executed     bool   `json:"executed"`
	BridgeTxID   string `json:"bridge_tx_id"`
	TargetTxHash string `json:"target_tx_hash,omitempty"`
	Message      string `json:"message"`
}

// SetGuardian 配置本節點為跨鏈守衛：簽名身份 + 全網守衛列表 + 多簽驗證器。
// validators 的 Address/PublicKey 須與各守衛節點自身密鑰對應。
func (b *Bridge) SetGuardian(kp *crypto.KeyPair, validators []Validator) error {
	if kp == nil {
		return fmt.Errorf("bridge: 守衛密鑰為空")
	}
	if len(validators) < b.cfg.RequiredSignatures {
		return fmt.Errorf("bridge: 守衛數量 %d 少於簽名門檻 %d",
			len(validators), b.cfg.RequiredSignatures)
	}
	addr, err := kp.Address()
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.guardianKP = kp
	b.guardianAddr = addr
	b.validators = validators
	b.verifier = NewMessageVerifier(b.cfg.RequiredSignatures)
	b.mu.Unlock()

	// 冪等註冊守衛到本地持久層。
	for _, v := range validators {
		if err := b.AddValidator(v); err != nil {
			return err
		}
	}
	return nil
}

// GuardianAddress 返回本節點守衛地址（未配置則空串）。
func (b *Bridge) GuardianAddress() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.guardianAddr
}

// NetworkedTransfer 網絡化跨鏈發起：本地建 pending 記錄並返回提案消息與結果。
// direction 為 "lock"（正向鎖定-鑄造）或 "burn"（反向銷毀-解鎖）。
// sourceTxHash 為源鏈鎖定/銷毀證明哈希，供守衛網絡核驗。
// proposer 為提案方守衛地址（聚合達標後由它執行目標鏈動作）。
func (b *Bridge) NetworkedTransfer(direction, sourceChain, targetChain,
	sourceAddress, targetAddress string, amount float64, token, sourceTxHash,
	proposer string) (*CrossChainMessage, *ActionResult, error) {
	if direction != "lock" && direction != "burn" {
		return nil, nil, fmt.Errorf("bridge: 未知方向 %s", direction)
	}
	res, err := b.createTransfer(sourceChain, targetChain,
		sourceAddress, targetAddress, amount, token)
	if err != nil {
		return nil, nil, err
	}
	tx, err := b.store.GetBridgeTx(res.BridgeTxID)
	if err != nil || tx == nil {
		return nil, nil, fmt.Errorf("bridge: 讀取本地記錄失敗: %w", err)
	}
	mtype := MsgLock
	if direction == "burn" {
		mtype = MsgBurn
	}
	payload := map[string]any{
		"bridge_tx_id":   res.BridgeTxID,
		"source_address": sourceAddress,
		"target_address": targetAddress,
		"amount":         amount,
		"token":          token,
		"source_tx_hash": sourceTxHash,
		"direction":      direction,
		"proposer":       proposer,
	}
	m, err := NewMessage(sourceChain, targetChain, mtype, payload, tx.Nonce)
	if err != nil {
		return nil, nil, err
	}
	return m, res, nil
}

// HandleProposal 守衛處理跨鏈提案：驗證格式/鏈/金額 → 本地冪等建立 pending
// 記錄 → 若本節點是守衛則簽名返回（供網絡廣播）。
func (b *Bridge) HandleProposal(m *CrossChainMessage) (*ValidatorSig, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.verifier == nil {
		return nil, fmt.Errorf("bridge: 守衛網絡未配置")
	}
	if err := b.validateProposal(m); err != nil {
		return nil, err
	}
	if err := b.ensurePending(m); err != nil {
		return nil, err
	}
	if b.guardianKP == nil {
		return nil, nil
	}
	// 操作副本並緩存：傳入對象可能正被網絡層序列化轉發，不得原地修改。
	work := cloneMessage(m)
	b.proposals[m.MessageID] = work
	sig, err := work.Sign(b.guardianKP)
	if err != nil {
		return nil, err
	}
	// 本節點簽名納入聚合基準（自己的簽名也是有效一票）。
	work.AddSignature(sig)
	return &sig, nil
}

// HandleSignature 收集守衛簽名：達 required 且多簽驗證通過後，由提案方
// 執行目標鏈動作並返回結果；非提案方僅收斂（等待 exec 廣播）。
func (b *Bridge) HandleSignature(m *CrossChainMessage, sig *ValidatorSig) (*ExecResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.verifier == nil {
		return nil, fmt.Errorf("bridge: 守衛網絡未配置")
	}
	if m == nil || sig == nil || sig.Validator == "" {
		return nil, fmt.Errorf("bridge: 無效簽名消息")
	}
	// 以本地緩存提案為聚合基準（網絡 sig 可能不帶消息體）。
	cached, ok := b.proposals[m.MessageID]
	if ok {
		m = cached
	} else {
		// 未收過提案（僅收到簽名）：克隆一份作為聚合基準，不修改傳入對象。
		m = cloneMessage(m)
		b.proposals[m.MessageID] = m
	}
	for _, s := range m.Signatures {
		if s.Validator == sig.Validator {
			return nil, nil // 已收集，幂等
		}
	}
	m.AddSignature(*sig)

	res, err := b.verifier.Verify(m, b.validators)
	if err != nil {
		return nil, err
	}
	log.Printf("[bridge] 聚合 mid=%s 簽名數=%d 有效=%d ok=%v %s",
		m.MessageID, len(m.Signatures), res.SignaturesVerified, res.OK, res.Error)
	if !res.OK {
		return nil, nil // 未達門檻，等待更多簽名
	}

	proposer, _ := m.Payload["proposer"].(string)
	if proposer != b.guardianAddr {
		return &ExecResult{
			Executed:   false,
			BridgeTxID: bridgeTxIDOf(m),
			Message:    "signatures reach threshold; awaiting executor",
		}, nil
	}
	return b.executeProposal(m)
}

// ApplyExecuted 非提案方收到執行結果後冪等收斂本地狀態（pending/locked/burning → confirmed）。
func (b *Bridge) ApplyExecuted(bridgeTxID, targetTxHash string) (*BridgeTx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tx, err := b.store.GetBridgeTx(bridgeTxID)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("bridge: 跨鏈交易不存在")
	}
	if tx.Status == StatusConfirmed {
		return tx, nil // 冪等
	}
	now := time.Now().Unix()
	tx.Status = StatusConfirmed
	tx.TargetTxHash = targetTxHash
	tx.ConfirmedAt = now
	tx.UpdatedAt = now
	if err := b.store.UpdateBridgeTx(tx); err != nil {
		return nil, err
	}
	return tx, nil
}

// validateProposal 檢查提案基本合法性（不含簽名）。
func (b *Bridge) validateProposal(m *CrossChainMessage) error {
	if m == nil || m.MessageID == "" {
		return fmt.Errorf("bridge: 空提案")
	}
	if err := b.validateChains(m.SourceChain, m.TargetChain); err != nil {
		return err
	}
	direction, _ := m.Payload["direction"].(string)
	if direction != "lock" && direction != "burn" {
		return fmt.Errorf("bridge: 未知方向 %q", direction)
	}
	amount, err := payloadFloat(m.Payload, "amount")
	if err != nil {
		return err
	}
	minAmt, err := b.minAmount()
	if err != nil {
		return err
	}
	if amount < minAmt {
		return fmt.Errorf("bridge: 金額低於下限 %g", minAmt)
	}
	if time.Now().Unix()-m.Ts > 86400 {
		return fmt.Errorf("bridge: 提案已過期（>24h）")
	}
	return nil
}

// ensurePending 冪等建立本地 pending 記錄（其他守衛節點也可能收到提案）。
func (b *Bridge) ensurePending(m *CrossChainMessage) error {
	id := bridgeTxIDOf(m)
	if id == "" {
		return fmt.Errorf("bridge: 提案缺少 bridge_tx_id")
	}
	if tx, _ := b.store.GetBridgeTx(id); tx != nil {
		return nil
	}
	src, _ := m.Payload["source_address"].(string)
	tgt, _ := m.Payload["target_address"].(string)
	amount, err := payloadFloat(m.Payload, "amount")
	if err != nil {
		return err
	}
	token, _ := m.Payload["token"].(string)
	if token == "" {
		token = "TACM"
	}
	rate, err := b.feeRate()
	if err != nil {
		return err
	}
	fee := round8(amount * rate)
	now := time.Now().Unix()
	tx := &BridgeTx{
		BridgeTxID:     id,
		SourceChain:    m.SourceChain,
		TargetChain:    m.TargetChain,
		SourceAddress:  src,
		TargetAddress:  tgt,
		Amount:         amount,
		Fee:            fee,
		ReceivedAmount: round8(amount - fee),
		Token:          token,
		Status:         StatusPending,
		Nonce:          m.Nonce,
		CreatedAt:      now,
		UpdatedAt:      now,
		Signatures:     []ValidatorSig{},
	}
	return b.store.CreateBridgeTx(tx)
}

// executeProposal 提案方執行目標鏈動作：正向 鎖定→鑄造；反向 銷毀→解鎖。
// 直接以 store 原子推進（本方法在 b.mu 持鎖下被調用，不得再調加鎖公共方法）。
func (b *Bridge) executeProposal(m *CrossChainMessage) (*ExecResult, error) {
	id := bridgeTxIDOf(m)
	tx, err := b.store.GetBridgeTx(id)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, fmt.Errorf("bridge: 跨鏈交易不存在")
	}
	if tx.Status == StatusConfirmed {
		return &ExecResult{Executed: true, BridgeTxID: id,
			TargetTxHash: tx.TargetTxHash, Message: "already executed"}, nil
	}
	if tx.Status != StatusPending {
		return nil, fmt.Errorf("bridge: 無法從 %s 執行", tx.Status)
	}
	srcTx, _ := m.Payload["source_tx_hash"].(string)
	targetTx := "target-" + hex.EncodeToString(
		crypto.SHA256([]byte(m.MessageID + ":" + srcTx)))[:32]
	now := time.Now().Unix()
	tx.Status = StatusConfirmed
	tx.SourceTxHash = srcTx
	tx.TargetTxHash = targetTx
	if direction, _ := m.Payload["direction"].(string); direction == "lock" {
		tx.LockTxHash = srcTx
	} else {
		tx.UnlockTxHash = targetTx
	}
	// 持久化守衛聚合簽名（可審計：由誰簽名批准了此次執行）。
	if len(m.Signatures) > 0 {
		tx.Signatures = append([]ValidatorSig(nil), m.Signatures...)
	}
	tx.ConfirmedAt = now
	tx.UpdatedAt = now
	if err := b.store.UpdateBridgeTx(tx); err != nil {
		return nil, err
	}
	return &ExecResult{
		Executed:     true,
		BridgeTxID:   id,
		TargetTxHash: targetTx,
		Message:      "guardian threshold met; executed on target",
	}, nil
}

func bridgeTxIDOf(m *CrossChainMessage) string {
	id, _ := m.Payload["bridge_tx_id"].(string)
	return id
}

// payloadFloat 讀取 payload 中的數字字段（兼容 float64/json.Number/string 表示）。
func payloadFloat(payload map[string]any, key string) (float64, error) {
	switch v := payload[key].(type) {
	case float64:
		return v, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, err
		}
		return f, nil
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("bridge: 字段 %s 非數字", key)
	}
}

// cloneMessage 深拷貝跨鏈消息（JSON 快照）：網絡層轉發與本地聚合並行，
// 任何修改都應發生在副本上以避免數據競爭。
func cloneMessage(m *CrossChainMessage) *CrossChainMessage {
	if m == nil {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var out CrossChainMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return &out
}
