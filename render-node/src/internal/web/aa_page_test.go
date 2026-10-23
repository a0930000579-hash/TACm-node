package web

// M154：AA 頁面（GET /aa）渲染煙霧測試。僅需 Status()；其餘 DataSource 方法回零值。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tacm/internal/chaindb"
	"tacm/internal/exchange"
	"tacm/internal/node"
)

// aaPageDS 最小 DataSource：render 對 /aa 只呼叫 Status()。
type aaPageDS struct{}

func (aaPageDS) Status() StatusView {
	return StatusView{Height: 1, FinalizedHeight: 1, Address: "tx0-test"}
}
func (aaPageDS) Blocks(int) ([]BlockView, error)          { return nil, nil }
func (aaPageDS) Block(int64) (*BlockView, error)          { return nil, nil }
func (aaPageDS) Transaction(string) (*TxView, error)      { return nil, nil }
func (aaPageDS) Address(string) (*AddressView, error)     { return nil, nil }
func (aaPageDS) Wallet(string) (*WalletView, error)       { return nil, nil }
func (aaPageDS) ChainStats() ChainStatsView               { return ChainStatsView{} }
func (aaPageDS) Snapshot() (*node.SnapshotData, error)    { return nil, nil }
func (aaPageDS) RecentTransactions(int) ([]TxView, error) { return nil, nil }
func (aaPageDS) TxsByAddressPage(string, int, int, string) ([]TxView, error) {
	return nil, nil
}
func (aaPageDS) CountTxsByAddress(string, string) (int64, error) { return 0, nil }
func (aaPageDS) TxsCSV(string, string) (string, error)           { return "", nil }
func (aaPageDS) CurrentUser(*http.Request) (*node.AuthUser, error) {
	return nil, nil
}
func (aaPageDS) Logs(f chaindb.LogFilter) ([]chaindb.LogRow, error) { return nil, nil }
func (aaPageDS) FaucetClaim(string, string) (string, string, error) {
	return "", "", nil
}
func (aaPageDS) BlockDetail(int64) (*node.BlockDetail, error) { return nil, nil }
func (aaPageDS) OnChainCoinbase() (node.CoinbaseAggregate, error) {
	return node.CoinbaseAggregate{}, nil
}
func (aaPageDS) VerifyLedgerOnChain() (node.LedgerAuditResult, error) {
	return node.LedgerAuditResult{}, nil
}
func (aaPageDS) PendingTxHashes(int) ([]string, error) { return nil, nil }
func (aaPageDS) ExchangeMarketTicks() ([]exchange.MarketQuote, error) {
	return nil, nil
}
func (aaPageDS) NodeAddress() string                      { return "tx0-test" }
func (aaPageDS) NodeBalance() string                      { return "" }
func (aaPageDS) NodeRewardStats() chaindb.NodeRewardStats { return chaindb.NodeRewardStats{} }
func (aaPageDS) NodeDataDir() string                      { return "" }
func (aaPageDS) NodeSendFromNode(string, string) (string, string, error) {
	return "", "", nil
}
func (aaPageDS) NodeRegistry() ([]chaindb.NodeRegistry, error) { return nil, nil }
func (aaPageDS) ExchangeOrderBook(string, int) ([]exchange.PriceLevel, []exchange.PriceLevel, error) {
	return nil, nil, nil
}
func (aaPageDS) ExchangeRecentTrades(string, int) ([]exchange.Trade, error) {
	return nil, nil
}
func (aaPageDS) TipHeight() int64 { return 1 }

func TestAAPageRenders(t *testing.T) {
	srv, err := NewWithNetworkMode(aaPageDS{}, "mainnet", true, 1)
	if err != nil {
		t.Fatalf("建 web server 失敗: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	resp, err := hs.Client().Get(hs.URL + "/aa")
	if err != nil {
		t.Fatalf("GET /aa 失敗: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /aa 應 200，got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	// 關鍵元素：頁面標題、提交/估算/查詢按鈕、三個 RPC 端點的前端掛載點。
	for _, want := range []string{
		"data-i18n=\"aa.title\"",
		"id=\"aaSender\"",
		"id=\"aaEstimate\"",
		"id=\"aaSubmit\"",
		"id=\"aaStatusBtn\"",
		"/aa/estimate",
		"/aa/submit",
		"/aa/status",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("AA 頁面缺少關鍵元素: %q", want)
		}
	}
}
