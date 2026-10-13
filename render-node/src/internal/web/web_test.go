package web

import (
	"os"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tacm/internal/chaindb"
	"tacm/internal/crypto"
	"tacm/internal/node"
	"tacm/internal/exchange"
)

type fakeDS struct {
	st           StatusView
	blockList    []BlockView
	blockResult  *BlockView
	txResult     *TxView
	addrResult   *AddressView
	walletResult *WalletView
	txList       []TxView
}

func (f *fakeDS) Status() StatusView                                          { return f.st }
func (f *fakeDS) Blocks(int) ([]BlockView, error)                             { return f.blockList, nil }
func (f *fakeDS) Block(int64) (*BlockView, error)                             { return f.blockResult, nil }
func (f *fakeDS) Transaction(string) (*TxView, error)                         { return f.txResult, nil }
func (f *fakeDS) Address(string) (*AddressView, error)                        { return f.addrResult, nil }
func (f *fakeDS) Wallet(string) (*WalletView, error)                          { return f.walletResult, nil }
func (f *fakeDS) ChainStats() ChainStatsView                                  { return ChainStatsView{} }
func (f *fakeDS) RecentTransactions(int) ([]TxView, error)                    { return f.txList, nil }
func (f *fakeDS) CurrentUser(*http.Request) (*node.AuthUser, error)           { return nil, nil }
func (f *fakeDS) TxsByAddressPage(string, int, int, string) ([]TxView, error) { return f.txList, nil }
func (f *fakeDS) CountTxsByAddress(string, string) (int64, error)            { return int64(len(f.txList)), nil }
func (f *fakeDS) TxsCSV(string, string) (string, error)                       { return "tx_hash,from,to\n", nil }
func (f *fakeDS) BlockDetail(int64) (*node.BlockDetail, error)                { return nil, nil }
func (f *fakeDS) OnChainCoinbase() (node.CoinbaseAggregate, error) {
	return node.CoinbaseAggregate{Total: 100, Pool: 12, Miners: map[string]float64{"tx0proposeraddr": 88}, Count: 10, TipHeight: 10}, nil
}

// PendingTxHashes M85：交易池待打包哈希（測試替身）。
func (f *fakeDS) ExchangeMarketTicks() ([]exchange.MarketQuote, error) {
	return nil, errors.New("fake: exchange disabled")
}

func (f *fakeDS) ExchangeOrderBook(market string, depth int) (bids, asks []exchange.PriceLevel, err error) {
	return nil, nil, errors.New("fake: exchange disabled")
}

func (f *fakeDS) ExchangeRecentTrades(market string, limit int) ([]exchange.Trade, error) {
	return nil, errors.New("fake: exchange disabled")
}

func (f *fakeDS) PendingTxHashes(limit int) ([]string, error) { return nil, nil }

// Logs M128：事件日誌查詢（測試替身回空）。
func (f *fakeDS) Logs(chaindb.LogFilter) ([]chaindb.LogRow, error) { return nil, nil }
func (f *fakeDS) FaucetClaim(string, string) (string, string, error)  { return "", "", nil }

// M148：節點面板（測試替身回固定值）。
func (f *fakeDS) NodeAddress() string                                   { return "tx01TESTNODEADDR0000000000000000000000" }
func (f *fakeDS) NodeBalance() string                                   { return "100" }
func (f *fakeDS) NodeDataDir() string                                   { return os.TempDir() }
func (f *fakeDS) NodeSendFromNode(to, amount string) (string, string, error) { return "hash", "txid", nil }

// VerifyLedgerOnChain M71：交易與合約層審計（測試替身恆回傳全過）。
func (f *fakeDS) VerifyLedgerOnChain() (node.LedgerAuditResult, error) {
	return node.LedgerAuditResult{OK: true, LedgerOK: true, TipHeight: 10,
		TxChecked: 2, TxSigPass: 2, TxNoncePass: 2, TxBalancePass: 2,
		ContractPass: 1, CoinbaseTotal: 100, BalancesTotal: 100, Issues: []string{}}, nil
}

func newFake() *fakeDS {
	now := time.Now().Unix()
	return &fakeDS{
		st:        StatusView{Height: 10, FinalizedHeight: 10, Difficulty: 3, MempoolSize: 1},
		blockList: []BlockView{{Height: 10, Hash: "0123456789abcdef0123456789abcdef", ProposerAddress: "tx0proposeraddr", TxCount: 1, Ts: now}},
		blockResult: &BlockView{Height: 5, Hash: "blockhash5", ProposerAddress: "tx0proposeraddr",
			Transactions: []TxView{{Hash: "txhash1", To: "tx0recipient", Amount: "10"}}},
		txResult:   &TxView{Hash: "txhash1", BlockHeight: 5, To: "tx0recipient", Amount: "10", Status: "confirmed"},
		addrResult: &AddressView{Address: "tx0proposeraddr", Balance: "100", Transactions: []TxView{{Hash: "txhash1"}}},
		txList: []TxView{
			{Hash: "txhash1", BlockHeight: 5, From: "tx0proposeraddr", To: "tx0recipient", Amount: "10", Memo: "hello", Ts: now},
		},
		walletResult: &WalletView{
			Address: "tx0proposeraddr", TACm: "1250000000000000000", TiUSD: "5000000",
			USDT: "3000000", SyncedBlock: 8, FeeTiUSDBps: 50, FeeUSDTBps: 125, FeeTACmBps: 200,
			TiUSDSupply: 5000000, TiUSDMinted: 5000000,
			Ledger: []WalletLedgerView{{Ts: 1700000000, Kind: "reward", Asset: "TACm", Account: "tx0proposeraddr", Delta: "1000000000000000000", Memo: "block:1:0"}},
		},
	}
}

func TestServerPages(t *testing.T) {
	srv, err := New(newFake())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cases := []struct {
		path       string
		wantStatus int
		contains   string
	}{
		{"/", http.StatusOK, "TAC Ai Smart Chain"}, // M80：/ 為品牌門戶
		{"/app", http.StatusOK, "最新區塊"},            // M80：系統主頁移至 /app
		{"/explorer", http.StatusOK, "鏈上搜尋"},
		{"/explorer", http.StatusOK, "最新交易"},
		{"/join", http.StatusOK, "加入 TAC Ai 智能鏈主網"},
		{"/join", http.StatusOK, "2.28.201.174:8080"},
		{"/network", http.StatusOK, "節點監控"},
		{"/block/5", http.StatusOK, "blockhash5"},
		{"/address/tx0proposeraddr", http.StatusOK, "100"},
		{"/tx/txhash1", http.StatusOK, "txhash1"},
		{"/wallet", http.StatusOK, "Web3 錢包"},
		{"/wallet?address=tx0proposeraddr", http.StatusOK, "1250000000000000000"},
		{"/static/css/style.css", http.StatusOK, "var(--bg)"},
	}
	for _, c := range cases {
		resp, err := http.Get(ts.URL + c.path)
		if err != nil {
			t.Fatalf("GET %s: %v", c.path, err)
		}
		body := readAllString(resp)
		if resp.StatusCode != c.wantStatus {
			t.Errorf("GET %s 狀態=%d 想要 %d", c.path, resp.StatusCode, c.wantStatus)
		}
		if c.contains != "" && !strings.Contains(body, c.contains) {
			t.Errorf("GET %s 未包含 %q", c.path, c.contains)
		}
		resp.Body.Close()
	}
}

func TestServerErrors(t *testing.T) {
	f := newFake()
	f.blockResult = nil // 缺失區塊
	f.txResult = nil    // 缺失交易
	srv, _ := New(f)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 缺失區塊 → 404
	resp, _ := http.Get(ts.URL + "/block/999")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("缺失區塊 狀態=%d 想要 404", resp.StatusCode)
	}
	resp.Body.Close()

	// 非法高度 → 400
	resp, _ = http.Get(ts.URL + "/block/abc")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("非法高度 狀態=%d 想要 400", resp.StatusCode)
	}
	resp.Body.Close()

	// 缺失交易 → 404
	resp, _ = http.Get(ts.URL + "/tx/missing")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("缺失交易 狀態=%d 想要 404", resp.StatusCode)
	}
	resp.Body.Close()

	// 未知路徑 → 404
	resp, _ = http.Get(ts.URL + "/nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知路徑 狀態=%d 想要 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestExplorerStatsAPI M124：Explorer 首頁輪詢 API——統計卡＋最新區塊/交易 JSON。
func TestExplorerStatsAPI(t *testing.T) {
	srv, err := New(newFake())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/explorer/stats")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`"ok":true`, `"status":`, `"blocks":`, `"txs":`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("Explorer stats 缺 %s: %s", want, string(body))
		}
	}
}

// TestAddressTxsAPI M123：地址交易分頁 API——total、offset 分頁與 dir 篩選。
func TestAddressTxsAPI(t *testing.T) {
	srv, err := New(newFake())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// fakeDS 有 1 筆交易 → total=1、limit=10 一頁內回一筆。
	resp, err := http.Get(ts.URL + "/api/address/txs?address=tx0alice&offset=0&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"total":1`) {
		t.Errorf("total 缺失或錯誤: %s", string(body))
	}
	if !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("ok 缺失: %s", string(body))
	}
	if !strings.Contains(string(body), `"txhash1"`) {
		t.Errorf("交易清單缺失: %s", string(body))
	}

	// 缺 address → ok:false
	resp, _ = http.Get(ts.URL + "/api/address/txs")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"ok":false`) {
		t.Errorf("缺 address 應失敗: %s", string(body))
	}
}

// TestSearchAPI 驗證瀏覽器搜尋：數字→區塊、tx0 地址→地址、交易哈希→交易、其餘→none。
func TestSearchAPI(t *testing.T) {
	srv, _ := New(newFake())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 找不到交易的情境（交易哈希未命中 → none）。
	f2 := newFake()
	f2.txResult = nil
	srv2, _ := New(f2)
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()

	cases := []struct {
		q    string
		want string
		url  string
	}{
		{"123", `"type":"block"`, ts.URL},
		{"tx0proposeraddr", `"type":"address"`, ts.URL},
		{"txhash1", `"type":"tx"`, ts.URL},
		{"zzz_missing", `"type":"none"`, ts2.URL},
		{"", `"type":"none"`, ts.URL},
	}
	for _, c := range cases {
		resp, err := http.Get(c.url + "/api/search?q=" + c.q)
		if err != nil {
			t.Fatalf("GET /api/search?q=%s: %v", c.q, err)
		}
		body := readAllString(resp)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("q=%q 狀態=%d 想要 200", c.q, resp.StatusCode)
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("q=%q 未包含 %q（body=%s）", c.q, c.want, body)
		}
		resp.Body.Close()
	}
}

// TestPeersAPI 驗證公開 seed 列表：單一權威來源、任何入口看到的一致。
func TestPeersAPI(t *testing.T) {
	srv, _ := New(newFake())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/peers")
	if err != nil {
		t.Fatalf("GET /api/peers: %v", err)
	}
	body := readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("狀態=%d 想要 200", resp.StatusCode)
	}
	for _, want := range []string{`"ok":true`, `"network":"mainnet"`, `"chain_id":"tacm-mainnet-1"`, `2.28.201.174:8080`, `tacm-ai-cyprto.onrender.com`} {
		if !strings.Contains(body, want) {
			t.Errorf("未包含 %q（body=%s）", want, body)
		}
	}
	resp.Body.Close()
}

// TestTestnetIsolation 驗證 testnet 網段（M65）：鏈 ID 獨立、seed 由 TACM_TESTNET_SEEDS 配置、
// /join 頁面顯示 testnet 指令（與主網完全分離、不混用）。
func TestTestnetIsolation(t *testing.T) {
	srv, _ := NewWithNetwork(newFake(), "testnet")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1) /api/peers：testnet 無配置 seed → 空列表、chain_id=tacm-testnet-1。
	resp, err := http.Get(ts.URL + "/api/peers")
	if err != nil {
		t.Fatalf("GET /api/peers: %v", err)
	}
	body := readAllString(resp)
	for _, want := range []string{`"ok":true`, `"network":"testnet"`, `"chain_id":"tacm-testnet-1"`, `"seeds":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("未包含 %q（body=%s）", want, body)
		}
	}
	resp.Body.Close()

	// 2) 配置 TACM_TESTNET_SEEDS 後，/api/peers 回傳 testnet seed（不與 mainnet 混淆）。
	t.Setenv("TACM_TESTNET_SEEDS", "http://10.0.0.1:8080, http://10.0.0.2:8080")
	srv2, _ := NewWithNetwork(newFake(), "testnet")
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()
	resp2, err := http.Get(ts2.URL + "/api/peers")
	if err != nil {
		t.Fatalf("GET /api/peers#2: %v", err)
	}
	body2 := readAllString(resp2)
	for _, want := range []string{`"node_id":"testnet-1"`, `10.0.0.1:8080`, `"node_id":"testnet-2"`, `10.0.0.2:8080`} {
		if !strings.Contains(body2, want) {
			t.Errorf("未包含 %q（body=%s）", want, body2)
		}
	}
	if strings.Contains(body2, `2.28.201.174:8080`) {
		t.Errorf("testnet seed 混入了 mainnet 錨點（body=%s）", body2)
	}
	resp2.Body.Close()

	// 3) /join 頁面：testnet 版指令（-network testnet）且不含 mainnet 錨點 seed。
	resp3, err := http.Get(ts2.URL + "/join")
	if err != nil {
		t.Fatalf("GET /join: %v", err)
	}
	body3 := readAllString(resp3)
	for _, want := range []string{`tacm-testnet-1`, `-network testnet`, `testnet-1`} {
		if !strings.Contains(body3, want) {
			t.Errorf("/join 未包含 %q", want)
		}
	}
	resp3.Body.Close()
}

// TestNetworkAPI 驗證全網監控：seed 探測（Goroutine 並行）＋本節點狀態＋離線容錯。
func TestNetworkAPI(t *testing.T) {
	// 模擬一個線上 seed：回真實 /status JSON。
	mockOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"node_id":"mock1","address":"tx0mockaddr","network":"mainnet","chain_id":"tacm-mainnet-1","block_height":42,"final_block_height":42,"mempool_size":0,"effective_difficulty":3,"uptime_sec":100,"consensus":"pow_dynamic"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockOK.Close()

	// 模擬一個離線 seed：直接關閉的 server（連不上）。
	mockDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	mockDown.Close()

	srv, _ := New(newFake())
	srv.seedURLs = []SeedInfo{
		{NodeID: "mock1", URL: mockOK.URL, Role: "test"},
		{NodeID: "mock2", URL: mockDown.URL, Role: "test"},
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/network")
	if err != nil {
		t.Fatalf("GET /api/network: %v", err)
	}
	body := readAllString(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("狀態=%d 想要 200", resp.StatusCode)
	}
	for _, want := range []string{
		`"ok":true`, `"network":"mainnet"`, `"chain_id":"tacm-mainnet-1"`,
		`"node_id":"mock1"`, `"reachable":true`, `"block_height":42`,
		`"node_id":"mock2"`, `"reachable":false`, `"error":"unreachable"`,
		`"self"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("未包含 %q（body=%s）", want, body)
		}
	}
	resp.Body.Close()
}

func readAllString(resp *http.Response) string {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}

func TestParseContractMemo(t *testing.T) {
	cases := []struct {
		memo string
		kind string
		gas  uint64
		data string
		nil  bool
	}{
		{"", "", 0, "", true},
		{"hello", "", 0, "", true},
		{"vm:deploy:5000000:6001", "deploy", 5000000, "6001", false},
		{"vm:call:100:aa11bb", "call", 100, "aa11bb", false},
		{"vm:deploy:6001", "deploy", 0, "6001", false}, // 舊格式無 gas
		{"vm:call:", "call", 0, "", false},             // 空 calldata 合法
	}
	for _, c := range cases {
		got := parseContractMemo(c.memo)
		if c.nil {
			if got != nil {
				t.Errorf("memo %q 應為 nil，得 %+v", c.memo, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("memo %q 應解析，得 nil", c.memo)
			continue
		}
		if got.Kind != c.kind || got.Gas != c.gas || got.Data != c.data {
			t.Errorf("memo %q → %+v, want kind=%s gas=%d data=%s",
				c.memo, got, c.kind, c.gas, c.data)
		}
	}
}

// M88：助記詞 API e2e（generate→restore roundtrip＋錯誤處理）。
func TestMnemonicAPI(t *testing.T) {
	srv, _ := New(newFake())
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	wif, _ := kp.WIF()

	// generate
	body := strings.NewReader(`{"wif":"` + wif + `"}`)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/wallet/mnemonic/generate", body)
	srv.handleMnemonicGenerate(rr, req)
	var gen struct {
		Mnemonic string `json:"mnemonic"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &gen); err != nil || gen.Mnemonic == "" {
		t.Fatalf("generate 失敗: %s body=%s", err, rr.Body.String())
	}
	if strings.Fields(gen.Mnemonic) == nil || len(strings.Fields(gen.Mnemonic)) != 24 {
		t.Fatalf("助記詞詞數錯誤: %q", gen.Mnemonic)
	}

	// restore
	body2 := strings.NewReader(`{"mnemonic":"` + gen.Mnemonic + `"}`)
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/wallet/mnemonic/restore", body2)
	srv.handleMnemonicRestore(rr2, req2)
	var rst struct {
		WIF     string `json:"wif"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &rst); err != nil || rst.WIF != wif {
		t.Fatalf("restore 失敗: err=%v wif=%q want=%q", err, rst.WIF, wif)
	}
	if !strings.HasPrefix(rst.Address, "tx0") {
		t.Fatalf("地址格式錯誤: %q", rst.Address)
	}

	// 錯誤：壞助記詞
	rr3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("POST", "/api/wallet/mnemonic/restore", strings.NewReader(`{"mnemonic":"bad word list here"}`))
	srv.handleMnemonicRestore(rr3, req3)
	var errResp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rr3.Body.Bytes(), &errResp)
	if errResp.Error == "" {
		t.Fatal("壞助記詞應回 error")
	}
}
