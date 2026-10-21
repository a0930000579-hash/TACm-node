package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// TestRelayerChainNameIsolation 驗證 M150h 多鏈擴展：
// 不同 ChainName 的 relayer 只處理自己目標鏈的 locked 交易（bsc 不碰 ethereum，反之亦然）。
func TestRelayerChainNameIsolation(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(filepath.Join(dir, "bridge")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	st := &bscStub{t: t, chainID: "0x61", nonce: "0x0", gasPrice: "0x1",
		estimate: "0x5208", sendRawH: "0x" + strings.Repeat("f1", 32),
		tokenAddr: "0x" + "11" + strings.Repeat("00", 18) + "22",
		lockProxy: "0x" + "33" + strings.Repeat("00", 18) + "44"}
	url := startBSCStub(t, st)

	newRelayer := func(chainName string) *Relayer {
		cfg := DefaultRelayerConfig()
		cfg.ChainName = chainName
		cfg.BSCTestRPC = url
		cfg.BSCPrivateKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		cfg.BSCTokenAddr = st.tokenAddr
		cfg.BSCLockProxyAddr = st.lockProxy
		cfg.BSCChainID = 97
		cfg.PollInterval = time.Hour // 測試不自動輪詢
		r, err := NewRelayer(cfg, b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	rBSC := newRelayer("bsc")
	rETH := newRelayer("ethereum")

	src := crypto.Hash160ToAddress(crypto.Hash160([]byte("mc-src")))
	tgt := crypto.Hash160ToAddress(crypto.Hash160([]byte("mc-tgt")))
	// 兩筆 locked：一筆 tacm→bsc、一筆 tacm→ethereum。
	resBSC, err := b.LockAndMint("tacm", "bsc", src, tgt, 10, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLock(resBSC.BridgeTxID, "0x"+strings.Repeat("b1", 32)); err != nil {
		t.Fatal(err)
	}
	resETH, err := b.LockAndMint("tacm", "ethereum", src, tgt, 20, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLock(resETH.BridgeTxID, "0x"+strings.Repeat("b2", 32)); err != nil {
		t.Fatal(err)
	}

	// bsc relayer 一輪：只處理 tacm→bsc（1 筆 raw），ethereum 單保持 locked。
	if err := rBSC.relayTACtoBSC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.rawTxs) != 1 {
		t.Fatalf("bsc relayer raw 交易數=%d 期望 1（只應處理 bsc 目標鏈）", len(st.rawTxs))
	}
	ethStatus, err := b.Status(resETH.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if ethStatus.Status != StatusLocked {
		t.Fatalf("ethereum 單應保持 locked（bsc relayer 不得處理），got %s", ethStatus.Status)
	}

	// ethereum relayer 一輪：只處理 tacm→ethereum（raw 從 1→2）。
	if err := rETH.relayTACtoBSC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.rawTxs) != 2 {
		t.Fatalf("ethereum relayer raw 交易數=%d 期望 2", len(st.rawTxs))
	}
	ethStatus2, err := b.Status(resETH.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if ethStatus2.Status != StatusMinted && ethStatus2.Status != StatusConfirmed {
		t.Fatalf("ethereum 單應由 ethereum relayer mint，got %s", ethStatus2.Status)
	}
}

// TestAssetsFromEnvNamed 驗證 M150h 多鏈資產表（TAC_<CHAIN>_ASSETS）可獨立載入。
func TestAssetsFromEnvNamed(t *testing.T) {
	t.Setenv("TAC_ETH_ASSETS", `[{"token":"tacm","bsc_token":"0x1111111111111111111111111111111111111111","bsc_lock_proxy":"0x2222222222222222222222222222222222222222","min_amount":0.001}]`)
	assets := AssetsFromEnvNamed("TAC_ETH_ASSETS")
	if len(assets) != 1 || assets[0].Token != "tacm" {
		t.Fatalf("TAC_ETH_ASSETS 載入失敗: %+v", assets)
	}
	// 預設 BSC 表不受影響。
	if got := AssetsFromEnvNamed("TAC_BSC_ASSETS"); len(got) != 0 {
		t.Fatalf("TAC_BSC_ASSETS 應為空, got %+v", got)
	}
}
