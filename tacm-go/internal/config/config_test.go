package config

import (
	"os"
	"testing"
)

// TestTestnetSeparatesDataDir 驗證 M41 主網/測試網分離：未顯式指定
// data-dir 時，testnet 自動使用獨立目錄與獨立鏈 ID，避免與主網帳本混用。
func TestTestnetSeparatesDataDir(t *testing.T) {
	os.Setenv("TACM_NETWORK", "testnet")
	t.Cleanup(func() { os.Unsetenv("TACM_NETWORK") })
	os.Unsetenv("TACM_DATA_DIR")
	os.Unsetenv("DATA_DIR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 錯誤: %v", err)
	}
	if cfg.Network != "testnet" {
		t.Fatalf("Network 應為 testnet，得到 %q", cfg.Network)
	}
	if cfg.DataDir != "./data-testnet" {
		t.Fatalf("testnet 資料目錄應分離為 ./data-testnet，得到 %q", cfg.DataDir)
	}
	if cfg.ChainID != "tacm-testnet-1" {
		t.Fatalf("testnet 鏈 ID 應為 tacm-testnet-1，得到 %q", cfg.ChainID)
	}
}

// TestMainnetKeepsDefaults 驗證 mainnet（預設）保持原目錄與鏈 ID。
func TestMainnetKeepsDefaults(t *testing.T) {
	os.Unsetenv("TACM_NETWORK")
	os.Unsetenv("TACM_DATA_DIR")
	os.Unsetenv("DATA_DIR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 錯誤: %v", err)
	}
	if cfg.Network != "mainnet" {
		t.Fatalf("Network 應為 mainnet，得到 %q", cfg.Network)
	}
	if cfg.DataDir != "./data" {
		t.Fatalf("mainnet 資料目錄應保持 ./data，得到 %q", cfg.DataDir)
	}
	if cfg.ChainID != "tacm-mainnet-1" {
		t.Fatalf("mainnet 鏈 ID 應為 tacm-mainnet-1，得到 %q", cfg.ChainID)
	}
}

// TestExplicitDataDirOverridesSeparation 驗證顯式指定 data-dir 時不做網段改寫。
func TestExplicitDataDirOverridesSeparation(t *testing.T) {
	os.Setenv("TACM_NETWORK", "testnet")
	os.Setenv("TACM_DATA_DIR", "/custom/data")
	t.Cleanup(func() {
		os.Unsetenv("TACM_NETWORK")
		os.Unsetenv("TACM_DATA_DIR")
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 錯誤: %v", err)
	}
	if cfg.DataDir != "/custom/data" {
		t.Fatalf("顯式 data-dir 不應被改寫，得到 %q", cfg.DataDir)
	}
}
