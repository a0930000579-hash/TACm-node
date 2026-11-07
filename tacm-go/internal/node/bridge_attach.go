package node

import (
	"path/filepath"

	bridgepkg "tacm/internal/bridge"
)

// AttachBridge 在本節點啟用跨鏈橋（Lock&Mint / Burn&Unlock）。
func (n *Node) AttachBridge() error {
	b, err := bridgepkg.New(
		bridgepkg.DefaultConfig(filepath.Join(n.cfg.DataDir, "bridge")))
	if err != nil {
		return err
	}
	n.bridge = b
	return nil
}
