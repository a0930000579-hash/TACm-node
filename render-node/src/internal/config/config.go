// Package config 提供 TAC Ai 智能鏈的統一配置（單一來源：環境變量 + 默認值）。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 為全節點 / Web 服務的運行配置。
type Config struct {
	ChainID   string // 自主鏈 ID，例如 tacm-mainnet-1
	Network   string // mainnet | testnet
	BlockTime int    // 目標出塊間隔（秒）

	RPCHost string // RPC / HTTP 監聽地址
	RPCPort int    // RPC 端口
	P2PPort int    // P2P 端口

	DataDir  string // 鏈與業務數據目錄
	LogDir   string // 日誌目錄
	LogLevel string // debug | info | warn | error

	AddrHRP string // 地址人類可讀前綴（tx0）

	EnableL2 bool // 啟用 Layer2 Optimistic Rollup

	EnableBridge bool // 啟用跨鏈橋（Lock & Mint / Burn & Unlock）

	GenesisValidators string // 創世驗證人：nodeID:address:pubkey:power，逗號分隔
}

// Default 返回帶有安全默認值的配置。
func Default() *Config {
	return &Config{
		ChainID:   "tacm-mainnet-1",
		Network:   "mainnet",
		BlockTime: 60,
		RPCHost:   "0.0.0.0",
		RPCPort:   8545,
		P2PPort:   9001,
		DataDir:   "./data",
		LogDir:    "./logs",
		LogLevel:  "info",
		AddrHRP:   "tx0",
	}
}

// Load 從環境變量加載配置（缺失項使用默認值），並做合法性校驗。
func Load() (*Config, error) {
	c := Default()

	c.ChainID = envStr("CHAIN_ID", c.ChainID)
	c.Network = strings.ToLower(envStr("TACM_NETWORK", c.Network))
	c.BlockTime = envInt("BLOCK_TIME", c.BlockTime)

	c.RPCHost = envStr("RPC_HOST", c.RPCHost)
	c.RPCPort = envInt("RPC_PORT", c.RPCPort)
	c.P2PPort = envInt("TACM_P2P_PORT", c.P2PPort)

	c.DataDir = envStr("TACM_DATA_DIR", envStr("DATA_DIR", c.DataDir))
	c.LogDir = envStr("TACM_LOG_DIR", c.LogDir)
	c.LogLevel = strings.ToLower(envStr("TACM_LOG_LEVEL", c.LogLevel))

	// M41：主網/測試網資料目錄分離——未顯式指定 data-dir 時，testnet 使用獨立目錄，
	// 避免主網/測試網帳本混用（鏈 ID 亦隨網段區分）。
	if c.Network == "testnet" && c.DataDir == "./data" {
		c.DataDir = "./data-testnet"
	}
	if c.Network == "testnet" && strings.HasPrefix(c.ChainID, "tacm-mainnet") {
		c.ChainID = "tacm-testnet-1"
	}

	c.AddrHRP = strings.ToLower(strings.TrimSpace(envStr("TAC_ADDR_HRP", c.AddrHRP)))

	if v := strings.ToLower(strings.TrimSpace(envStr("TACM_ENABLE_L2", ""))); v == "1" || v == "true" {
		c.EnableL2 = true
	}
	if v := strings.ToLower(strings.TrimSpace(envStr("TACM_ENABLE_BRIDGE", ""))); v == "1" || v == "true" {
		c.EnableBridge = true
	}
	c.GenesisValidators = envStr("GENESIS_VALIDATORS", c.GenesisValidators)

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate 校驗配置的關鍵字段。
func (c *Config) Validate() error {
	if c.ChainID == "" {
		return fmt.Errorf("config: chain_id 不能為空")
	}
	switch c.Network {
	case "mainnet", "testnet":
	default:
		return fmt.Errorf("config: 非法 network %q（僅允許 mainnet/testnet）", c.Network)
	}
	if c.BlockTime <= 0 {
		return fmt.Errorf("config: block_time 必須為正整數，當前=%d", c.BlockTime)
	}
	if c.RPCPort < 1 || c.RPCPort > 65535 {
		return fmt.Errorf("config: rpc_port 超出範圍：%d", c.RPCPort)
	}
	if c.P2PPort < 1 || c.P2PPort > 65535 {
		return fmt.Errorf("config: p2p_port 超出範圍：%d", c.P2PPort)
	}
	if c.AddrHRP == "" {
		return fmt.Errorf("config: 地址前綴不能為空")
	}
	return nil
}

// RPCAddr 返回 "host:port" 形式的監聽地址。
func (c *Config) RPCAddr() string {
	return fmt.Sprintf("%s:%d", c.RPCHost, c.RPCPort)
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}
