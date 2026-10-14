package node

// M129-A：主流鏈 RPC 基建方法（eth_syncing/protocolVersion/coinbase/accounts/
// maxPriorityFeePerGas/uncle）——相容主流工具（MetaMask、Ethers、Hardhat）的
// 節點能力探測與帳戶查詢。

import (
	"fmt"
	"strconv"
)

// ethSyncing 回報同步狀態：本節點無下游同步（已就緒）時回 false（主流語義）。
func (s *RPCServer) ethSyncing() any {
	st := s.node.GetStatus()
	return map[string]any{
		"startingBlock": hexBig(0),
		"currentBlock":  hexBig(st.BlockHeight),
		"highestBlock":  hexBig(st.BlockHeight),
	}
}

// ethProtocolVersion 回報節點協定版本（P2P 協定層級，非用戶端版本）。
func (s *RPCServer) ethProtocolVersion() string {
	return "0x41" // 65：與以太坊主網協定版本一致
}

// ethCoinbase 回報區塊獎勵收款地址（本節點礦工/驗證人地址，以太 0x 格式）。
func (s *RPCServer) ethCoinbase() (string, error) {
	if s.node.keypair == nil {
		return "", fmt.Errorf("node address unavailable")
	}
	addr, err := s.node.keypair.EthAddress()
	if err != nil {
		return "", err
	}
	return addr, nil
}

// ethAccounts 回報節點管理的帳戶清單（以太 0x 格式；與 personal_sign/eth_sign 簽名帳戶一致）。
func (s *RPCServer) ethAccounts() []string {
	if s.node.keypair == nil {
		return []string{}
	}
	addr, err := s.node.keypair.EthAddress()
	if err != nil {
		return []string{}
	}
	return []string{addr}
}

// ethMaxPriorityFeePerGas 回報建議優先小費（EIP-1559）：依 BaseFee 動態推導
// （baseFee×1%，下限 1 gwei）。
func (s *RPCServer) ethMaxPriorityFeePerGas() string {
	st := s.node.GetStatus()
	base := st.BaseFee
	if base <= 0 {
		base = 0.000000001 // 初始 base fee 基準
	}
	tip := base * 0.01
	if tip < 0.000000001 {
		tip = 0.000000001
	}
	return feeToWeiHex(tip)
}

// ethUncleByHashAndIndex 依區塊哈希＋索引查叔塊：本鏈無 uncle 機制，回 null（主流相容）。
func (s *RPCServer) ethUncleByHashAndIndex(_ []any) any {
	return nil
}

// ethUncleByNumberAndIndex 依區塊高度＋索引查叔塊：本鏈無 uncle 機制，回 null（主流相容）。
func (s *RPCServer) ethUncleByNumberAndIndex(_ []any) any {
	return nil
}

// netPeerCount 回報在線對等節點數（net 命名空間，主流工具網路偵測）。
func (s *RPCServer) netPeerCount() (string, error) {
	peers, err := s.node.db.GetPeers(true)
	if err != nil {
		return "0x0", err
	}
	return "0x" + strconv.FormatInt(int64(len(peers)), 16), nil
}
