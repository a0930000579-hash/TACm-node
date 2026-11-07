package bridge

import (
	"strings"
	"testing"
)

// TestRelayerMultiAssetConfig 多資產互通配置（M149r）：Assets 填充、默認回退、Token 校驗。
func TestRelayerMultiAssetConfig(t *testing.T) {
	// 資產 Token 為空應在 Assets 校驗攔截（不依賴 BSC RPC 可達性）。
	b, err := New(DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("橋建立失敗: %v", err)
	}
	defer b.Close()

	cfg := DefaultRelayerConfig()
	cfg.BSCTestRPC = "http://127.0.0.1:1"
	cfg.Assets = []AssetConfig{{
		Token: "", BSCTokenAddr: "0x1111111111111111111111111111111111111111",
		BSCLockProxyAddr: "0x2222222222222222222222222222222222222222",
	}}
	_, err = NewRelayer(cfg, b)
	if err == nil || !strings.Contains(err.Error(), "Token 為空") {
		t.Fatalf("Token 空應報錯: %v", err)
	}

	// 資產地址不完整校驗（Token 非空但缺 BSC 地址）——回退舊欄位仍空 → 報錯。
	cfg2 := DefaultRelayerConfig()
	cfg2.BSCTestRPC = "http://127.0.0.1:1"
	cfg2.Assets = []AssetConfig{{Token: "tiusd", BSCTokenAddr: "", BSCLockProxyAddr: ""}}
	_, err = NewRelayer(cfg2, b)
	if err == nil || !strings.Contains(err.Error(), "地址不完整") {
		t.Fatalf("地址不完整應報錯: %v", err)
	}

	// 多資產配置正確：tacm＋tiusd 都給完整地址——NewRelayer 應通過校驗
	// （BSC RPC 127.0.0.1:1 不可達會在後續連線層失敗，但配置校驗已過）。
	cfg3 := DefaultRelayerConfig()
	cfg3.BSCTestRPC = "http://127.0.0.1:1"
	cfg3.Assets = []AssetConfig{
		{Token: "tacm", BSCTokenAddr: "0x1111111111111111111111111111111111111111", BSCLockProxyAddr: "0x2222222222222222222222222222222222222222"},
		{Token: "tiusd", BSCTokenAddr: "0x3333333333333333333333333333333333333333", BSCLockProxyAddr: "0x4444444444444444444444444444444444444444"},
	}
	rel, err := NewRelayer(cfg3, b)
	if err != nil {
		t.Fatalf("多資產配置應通過: %v", err)
	}
	if rel == nil {
		t.Fatal("relayer 為 nil")
	}
	// contracts 應含兩資產。
	if len(rel.contracts) != 2 {
		t.Fatalf("contracts 應含 2 資產, got %d", len(rel.contracts))
	}
	if _, ok := rel.contracts["tacm"]; !ok {
		t.Fatal("缺 tacm 資產合約")
	}
	if _, ok := rel.contracts["tiusd"]; !ok {
		t.Fatal("缺 tiusd 資產合約")
	}
	// contractFor 依 token 選合約、缺省回退 tacm。
	if rel.contractFor("tiusd") != rel.contracts["tiusd"] {
		t.Fatal("contractFor(tiusd) 應回 tiusd 合約")
	}
	if rel.contractFor("usdt") != rel.contracts["tacm"] {
		t.Fatal("contractFor(未知) 應回退 tacm 合約")
	}
}

// TestAssetsFromEnv TAC_BSC_ASSETS 環境變數多資產解析（M149s：部署後免改碼）。
func TestAssetsFromEnv(t *testing.T) {
	t.Setenv("TAC_BSC_ASSETS", `[
	  {"token":"tiusd","bsc_token":"0x1111111111111111111111111111111111111111","bsc_lock_proxy":"0x2222222222222222222222222222222222222222","min_amount":1},
	  {"token":"usdt","bsc_token":"0x3333333333333333333333333333333333333333","bsc_lock_proxy":"0x4444444444444444444444444444444444444444","min_amount":5}
	]`)
	got := AssetsFromEnv()
	if len(got) != 2 {
		t.Fatalf("應解析 2 資產, got %d", len(got))
	}
	if got[0].Token != "tiusd" || got[1].Token != "usdt" {
		t.Fatalf("資產順序錯誤: %+v", got)
	}
	if !strings.HasPrefix(got[0].BSCTokenAddr, "0x") {
		t.Fatalf("地址應 0x 開頭: %s", got[0].BSCTokenAddr)
	}
	// 非法項過濾（token 空 / 地址非 0x）。
	t.Setenv("TAC_BSC_ASSETS", `[
	  {"token":"","bsc_token":"0x1111","bsc_lock_proxy":"0x2222"},
	  {"token":"tacm","bsc_token":"nope","bsc_lock_proxy":"0x2222"},
	  {"token":"tacm","bsc_token":"0x1111","bsc_lock_proxy":"0x2222"}
	]`)
	got2 := AssetsFromEnv()
	if len(got2) != 1 || got2[0].Token != "tacm" {
		t.Fatalf("應只保留 1 合法項, got %+v", got2)
	}
	// env 未設 → nil。
	t.Setenv("TAC_BSC_ASSETS", "")
	if AssetsFromEnv() != nil {
		t.Fatal("env 未設應回 nil")
	}
}
