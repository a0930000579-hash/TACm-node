package node

import (
	"fmt"
	"strings"

	bridgepkg "tacm/internal/bridge"
	"tacm/internal/p2p"
)

// AttachBridgeNetwork 啟用跨鏈橋並配置守衛網絡：本節點密鑰即守衛簽名身份。
func (n *Node) AttachBridgeNetwork(guardians []bridgepkg.Validator) error {
	if err := n.AttachBridge(); err != nil {
		return err
	}
	if err := n.bridge.SetGuardian(n.keypair, guardians); err != nil {
		return err
	}
	n.bridgeNetworked = true
	return nil
}

// ParseGuardians 解析守衛規格：addr:pubkey 逗號分隔。
func ParseGuardians(spec string) ([]bridgepkg.Validator, error) {
	var vals []bridgepkg.Validator
	seen := map[string]bool{}
	for i, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, pub, ok := strings.Cut(part, ":")
		if !ok || addr == "" || pub == "" {
			return nil, fmt.Errorf("node: 守衛規格格式錯誤（應 addr:pubkey）: %q", part)
		}
		if seen[addr] {
			return nil, fmt.Errorf("node: 守衛地址重複: %s", addr)
		}
		seen[addr] = true
		vals = append(vals, bridgepkg.Validator{
			Address: addr, Name: "guardian-" + strings.TrimPrefix(addr, "tx0"),
			PublicKey: pub, Active: true,
		})
		_ = i
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("node: 守衛列表為空")
	}
	return vals, nil
}

// handleIncomingBridge 分派跨鏈網絡消息：提案→簽名廣播；簽名→聚合（達標由提案方執行並廣播）；
// 執行結果→全網收斂。未啟用守衛網絡時忽略。
func (n *Node) handleIncomingBridge(bg *p2p.BridgeGossip) error {
	if !n.bridgeNetworked || n.bridge == nil {
		return nil
	}
	switch bg.Type {
	case "propose":
		if bg.Message == nil {
			return fmt.Errorf("node: 空跨鏈提案")
		}
		sig, err := n.bridge.HandleProposal(bg.Message)
		if err != nil {
			return err
		}
		if sig != nil && n.p2pNet != nil {
			n.p2pNet.BroadcastBridgeSig(bg.MessageID, sig, bg.Message)
		}
	case "sig":
		if bg.Message == nil || bg.Sig == nil {
			return fmt.Errorf("node: 空跨鏈簽名")
		}
		res, err := n.bridge.HandleSignature(bg.Message, bg.Sig)
		if err != nil {
			return err
		}
		if res != nil && res.Executed && n.p2pNet != nil {
			n.p2pNet.BroadcastBridgeExec(res.BridgeTxID, res.TargetTxHash, bg.MessageID)
		}
	case "exec":
		if bg.BridgeTxID == "" {
			return fmt.Errorf("node: 空跨鏈執行結果")
		}
		if _, err := n.bridge.ApplyExecuted(bg.BridgeTxID, bg.TargetTxHash); err != nil {
			return err
		}
	default:
		return fmt.Errorf("node: 未知跨鏈消息類型 %q", bg.Type)
	}
	return nil
}
