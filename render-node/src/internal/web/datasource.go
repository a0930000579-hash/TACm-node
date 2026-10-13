package web

import (
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"tacm/internal/chaindb"
	"tacm/internal/node"
	"tacm/internal/exchange"
	"tacm/internal/wallet"
)

// DataSource 抽象 Web 層所需的鏈數據，便於以節點實現或測試替身注入。
type DataSource interface {
	Status() StatusView
	Blocks(limit int) ([]BlockView, error)
	Block(height int64) (*BlockView, error)
	Transaction(hash string) (*TxView, error)
	Address(addr string) (*AddressView, error)
	Wallet(addr string) (*WalletView, error)
	ChainStats() ChainStatsView
	// M62：公開區塊瀏覽器——最新交易（掃鏈頂往下，任何節點同鏈結果一致）。
	RecentTransactions(limit int) ([]TxView, error)
	// M88：地址交易分頁（offset/limit/dir=in|out）與 CSV 匯出。
	TxsByAddressPage(address string, offset, limit int, dir string) ([]TxView, error)
	// M123：地址交易總數（分頁總頁數計算）。
	CountTxsByAddress(address, dir string) (int64, error)
	TxsCSV(address, dir string) (string, error)
	// M37：讀取登入會員（session），供錢包/首頁等頁面依會員綁定地址渲染。
	CurrentUser(r *http.Request) (*node.AuthUser, error)
	// M128：以太式 logs 查詢（eth_subscribe logs / Explorer 代幣轉移追蹤共用）。
	Logs(f chaindb.LogFilter) ([]chaindb.LogRow, error)
	// FaucetClaim 以節點金鑰向地址發送測試幣（M128-C 水龍頭）。
	FaucetClaim(to, amount string) (string, string, error)
	// M66：鏈上審計——回傳區塊頭與完整交易（含 coinbase 明細），供 /api/block/{h} 使用。
	BlockDetail(height int64) (*node.BlockDetail, error)
	// M68：鏈上真實聚合——掃鏈上 coinbase 交易，取代本地 chain_stats/Ledger 快照口徑。
	OnChainCoinbase() (node.CoinbaseAggregate, error)
	// M71：交易與合約層鏈上審計——全鏈賬本重放（簽名/nonce/餘額/合約格式/守恆）。
	VerifyLedgerOnChain() (node.LedgerAuditResult, error)
	// M85：交易池待打包交易哈希（WebSocket newPendingTransactions 訂閱數據源）。
	PendingTxHashes(limit int) ([]string, error)
	// M114：交易所行情快照（WebSocket market_update 推送數據源）。
	ExchangeMarketTicks() ([]exchange.MarketQuote, error)
	// M115：訂單簿與最新成交（WebSocket depth/trades 推送數據源）。
	ExchangeOrderBook(market string, depth int) (bids, asks []exchange.PriceLevel, err error)
	ExchangeRecentTrades(market string, limit int) ([]exchange.Trade, error)
}

type nodeDS struct{ n *node.Node }

// CurrentUser 讀取登入會員（session cookie）。
func (d *nodeDS) CurrentUser(r *http.Request) (*node.AuthUser, error) {
	return d.n.Auth().CurrentUser(r)
}

// Logs 查詢事件日誌（M128：eth_subscribe logs / 代幣轉移追蹤）。
func (d *nodeDS) Logs(f chaindb.LogFilter) ([]chaindb.LogRow, error) {
	return d.n.DB().GetLogs(f)
}

// FaucetClaim 以節點金鑰發送測試幣（M128-C 水龍頭）。
func (d *nodeDS) FaucetClaim(to, amount string) (string, string, error) {
	return d.n.SendFromNode(to, amount)
}

// PendingTxHashes 返回交易池中待打包交易的哈希列表（fee 降序，最多 limit 筆）。
func (d *nodeDS) ExchangeMarketTicks() ([]exchange.MarketQuote, error) {
	return d.n.ExchangeMarkets()
}

func (d *nodeDS) ExchangeOrderBook(market string, depth int) (bids, asks []exchange.PriceLevel, err error) {
	return d.n.ExchangeOrderBook(market, depth)
}

func (d *nodeDS) ExchangeRecentTrades(market string, limit int) ([]exchange.Trade, error) {
	return d.n.ExchangeRecentTrades(market, limit)
}

func (d *nodeDS) PendingTxHashes(limit int) ([]string, error) {
	pool, err := d.n.DB().GetMempool(limit)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pool))
	for _, m := range pool {
		if h, ok := m["tx_hash"].(string); ok && h != "" {
			out = append(out, h)
		}
	}
	return out, nil
}

// NewNodeDataSource 把運行中的節點適配為 Web 數據源。
func NewNodeDataSource(n *node.Node) DataSource { return &nodeDS{n: n} }

// BlockDetail 回傳區塊頭與完整交易（含 coinbase 瓜分明細，M66 鏈上審計用）。
func (d *nodeDS) BlockDetail(height int64) (*node.BlockDetail, error) {
	return d.n.GetBlockDetail(height)
}

// OnChainCoinbase 掃鏈上 coinbase 交易聚合（M68：鏈上真實總產出／礦工收益）。
func (d *nodeDS) OnChainCoinbase() (node.CoinbaseAggregate, error) {
	return d.n.OnChainCoinbaseAggregate()
}

// VerifyLedgerOnChain 全鏈賬本重放審計（M71：交易與合約層）。
func (d *nodeDS) VerifyLedgerOnChain() (node.LedgerAuditResult, error) {
	return d.n.VerifyLedgerOnChain()
}

// walletView 把錢包服務快照轉為 Web 視圖。
func (d *nodeDS) Wallet(addr string) (*WalletView, error) {
	if d.n.Wallet() == nil {
		return nil, nil // 錢包未啟用：頁面顯示提示
	}
	acc, err := d.n.Wallet().Balance(addr)
	if err != nil {
		return nil, err
	}
	led, err := d.n.Wallet().Ledger(30)
	if err != nil {
		return nil, err
	}
	sup, _ := d.n.Wallet().TiUSDSummary()
	synced, _ := d.n.Wallet().SyncedHeight()
	w := &WalletView{
		Address: addr,
		// M32：錢包只顯示個人資產（十進制縮放）——raw 不再外洩。
		TACm:        wallet.FormatAmountBig(acc.TACmBalance),
		TiUSD:       wallet.FormatAmountI64(acc.TiUSDBalance, wallet.AssetTiUSD),
		USDT:        wallet.FormatAmountI64(acc.USDTBalance, wallet.AssetUSDT),
		SyncedBlock: synced,
		FeeTiUSDBps: 50, FeeUSDTBps: 125, FeeTACmBps: 200,
		NodeAddress: d.n.Address(),
	}
	// TiUSD 供給（最小單位 → 十進制）。
	if sup != nil {
		w.TiUSDSupply = sup.Supply
		w.TiUSDMinted = sup.TotalMinted
		w.TiUSDBurned = sup.TotalBurned
	}
	for _, e := range led {
		// M33：帳本變動以十進制顯示（raw 不再外洩）；保留 +/− 符號。
		w.Ledger = append(w.Ledger, WalletLedgerView{
			Ts: e.Ts, Kind: string(e.Kind), Asset: string(e.Asset),
			Account: e.Account, Delta: formatDelta(e.Delta, string(e.Asset)), Memo: e.Memo,
		})
	}
	return w, nil
}

// formatDelta 帳本變動縮放（依資產精度），保留符號。
func formatDelta(delta, asset string) string {
	v, ok := new(big.Int).SetString(delta, 10)
	if !ok {
		return delta
	}
	neg := v.Sign() < 0
	if neg {
		v = new(big.Int).Neg(v)
	}
	var out string
	switch wallet.Asset(asset) {
	case wallet.AssetTiUSD:
		out = wallet.FormatAmountBig(v)
	default: // TACm/USDT 18 位
		out = wallet.FormatAmountBig(v)
	}
	if neg {
		out = "-" + out
	}
	return out
}

func txView(t *chaindb.Transaction) TxView {
	return TxView{
		Hash: t.TxHash, BlockHeight: t.BlockHeight, TxIndex: t.TxIndex,
		From: t.FromAddr, To: t.ToAddr,
		Amount: t.Amount, Fee: t.Fee, Nonce: t.Nonce, Ts: t.Ts,
		Memo: t.Memo, Status: t.Status,
		Signature: t.Signature, Pubkey: t.Pubkey,
		Contract: parseContractMemo(t.Memo),
		GasUsed:  t.GasUsed,
	}
}

// parseContractMemo 解析合約交易 memo（與節點 splitContractMemo 同格式）：
// vm:deploy:<gas>:<hex> / vm:call:<gas>:<hex> 新格式與 vm:deploy:<hex> /
// vm:call:<hex> 舊格式；非合約 memo 返回 nil。
func parseContractMemo(memo string) *ContractView {
	var kind, rest string
	switch {
	case strings.HasPrefix(memo, "vm:deploy:"):
		kind, rest = "deploy", memo[len("vm:deploy:"):]
	case strings.HasPrefix(memo, "vm:call:"):
		kind, rest = "call", memo[len("vm:call:"):]
	default:
		return nil
	}
	cv := &ContractView{Kind: kind}
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		if g, err := strconv.ParseUint(rest[:i], 10, 64); err == nil {
			cv.Gas = g
		}
		cv.Data = rest[i+1:]
	} else {
		cv.Data = rest
	}
	return cv
}

func blockView(b *chaindb.Block) BlockView {
	prev := ""
	if b.PrevHash != nil {
		prev = *b.PrevHash
	}
	bv := BlockView{
		Height: b.Height, Hash: b.Hash, PrevHash: prev, MerkleRoot: b.MerkleRoot,
		Proposer: b.Proposer, ProposerAddress: b.ProposerAddress, Ts: b.Ts,
		TxCount: b.TxCount, Difficulty: b.Difficulty, Nonce: b.Nonce, Size: b.Size,
		BaseFee: b.BaseFee, GasUsed: b.GasUsed, GasLimit: b.GasLimit, Burned: b.Burned,
	}
	if b.Height > 0 {
		bv.PrevHeight = b.Height - 1
	}
	return bv
}

// ChainStatsView 首頁全鏈儀表（與 /api/chain/stats 同口徑：總產出僅計 coinbase 出塊分配）。
type ChainStatsView struct {
	TotalMinedTacm string `json:"total_mined_tacm"`
	TiUSDSupply    string `json:"tiusd_supply"`
	RewardPool     string `json:"reward_pool"`
	OnlineMiners   int    `json:"online_miners"`
	TotalHashrate  string `json:"total_hashrate"`
}

// ChainStats 全鏈統計（M34）：總產出＝coinbase :miner/:proposer/:pool 累計；
// 獎勵池＝reward_pool 帳戶餘額；TiUSD＝供給；全網算力＝窗口內在線礦工 hashrate 和。
func (d *nodeDS) ChainStats() ChainStatsView {
	out := ChainStatsView{}
	if d.n.Wallet() == nil {
		return out
	}
	total, err := d.n.Wallet().Store().ChainMinedTacm()
	if err != nil {
		total = big.NewInt(0)
	}
	out.TotalMinedTacm = wallet.FormatAmountBig(total)
	if acc, err := d.n.Wallet().Balance(wallet.RewardPoolAddr); err == nil {
		out.RewardPool = wallet.FormatAmountBig(acc.TACmBalance)
	}
	if sup, err := d.n.Wallet().TiUSDSummary(); err == nil && sup != nil {
		// M38.1：TiUSD 最小單位為 1e6（micro），須以 TiUSD 精度縮放，不能用 TACm 的 1e18（FormatAmountBig）。
		out.TiUSDSupply = wallet.FormatAmountI64(sup.Supply, wallet.AssetTiUSD)
	}
	// 全網在線礦工＝鏈上視角（最近 60 塊內收到 coinbase 瓜分的礦工）：任何節點只要同步了
	// 同一條鏈，看到的總人數與總算力就完全一致（鏈上事實），不再用各節點本地 SQL
	// （本地 SQL 會造成「錨點 3 台、follower 1 台」的入口不一致）。
	addrs, nominalHr := d.OnChainMiners(60)
	out.OnlineMiners = len(addrs)
	if len(addrs) > 0 {
		// 總算力＝鏈上瓜分礦工的等效算力（每台標稱 5M：1vCPU×1M + 2vGPU×2M）。
		out.TotalHashrate = fmt.Sprintf("%.2fM", nominalHr/1e6)
	} else {
		out.TotalHashrate = "0.00M"
	}
	return out
}

// OnChainMiners 從鏈上反推「全網在線礦工」：掃鏈頂回溯 window 塊的 hb（心跳）交易
// （hb:on 的收款人＝在線礦工），收集唯一礦工地址與真實算力總和。
// M60：取代 M59 的「掃 coinbase 收款人×標稱 5M」近似——hb 交易攜帶礦工聲明的真實算力，
// 且剛開機尚未收到瓜分的礦工也正確計入。結果只依賴已同步區塊，任何節點看到的
// 礦工集合與總算力都一致。
func (d *nodeDS) OnChainMiners(window int64) ([]string, float64) {
	addrs, total, err := d.n.OnChainMiners(window)
	if err != nil {
		return nil, 0
	}
	return addrs, float64(total)
}

func (d *nodeDS) Status() StatusView {
	s := d.n.GetStatus()
	return StatusView{
		NodeID: s.NodeID, Address: s.Address, Height: s.BlockHeight,
		FinalizedHeight: d.n.FinalizedHeight(), Difficulty: s.Difficulty,
		MempoolSize: int(s.MempoolSize), UptimeSec: s.UptimeSec,
		Consensus: s.Consensus,
		BaseFee:   s.BaseFee, GasPrice: s.GasPrice, BlockGasLimit: s.BlockGasLimit,
		BurnedTotal: s.BurnedTotal,
	}
}

func (d *nodeDS) Blocks(limit int) ([]BlockView, error) {
	blocks, err := d.n.DB().GetBlocks(limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]BlockView, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockView(b))
	}
	return out, nil
}

func (d *nodeDS) Block(height int64) (*BlockView, error) {
	detail, err := d.n.GetBlockDetail(height)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, nil
	}
	bv := blockView(detail.Block)
	bv.Transactions = make([]TxView, 0, len(detail.Transactions))
	for _, t := range detail.Transactions {
		bv.Transactions = append(bv.Transactions, txView(t))
	}
	return &bv, nil
}

func (d *nodeDS) Transaction(hash string) (*TxView, error) {
	t, err := d.n.DB().GetTransaction(hash)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, nil
	}
	tv := txView(t)
	// M75-2：交易收據事件日誌（鏈上 logs 表）。
	logs, err := d.n.DB().GetLogs(chaindb.LogFilter{TxHash: hash})
	if err != nil {
		return nil, err
	}
	tv.Logs = make([]LogView, 0, len(logs))
	for i := range logs {
		tv.Logs = append(tv.Logs, LogView{
			Address: logs[i].Address,
			Topics:  logs[i].Topics,
			Data:    logs[i].Data,
		})
	}
	return &tv, nil
}

// RecentTransactions 回傳鏈上最新交易（M62）：自鏈頂向下掃區塊並由區塊內尾部往前收集，
// 只依賴已同步區塊——任何節點（錨點/follower）看到的列表與鏈上事實一致。
func (d *nodeDS) RecentTransactions(limit int) ([]TxView, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	out := make([]TxView, 0, limit)
	for h := d.n.DB().GetTipHeight(); h > 0 && len(out) < limit; h-- {
		txs, err := d.n.DB().GetTransactionsByBlock(h)
		if err != nil {
			return nil, err
		}
		for i := len(txs) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, txView(txs[i]))
		}
	}
	return out, nil
}

func (d *nodeDS) Address(addr string) (*AddressView, error) {
	av := &AddressView{Address: addr, Balance: "0"}
	acc, err := d.n.DB().GetAccount(addr)
	if err != nil {
		return nil, err
	}
	if acc != nil {
		av.Balance = acc.Balance
		av.Nonce = acc.Nonce
	}
	txs, err := d.n.DB().GetTransactionsByAddress(addr, 10) // M123：初始頁 10 筆，其餘走 /api/address/txs 分頁
	if err != nil {
		return nil, err
	}
	av.Transactions = make([]TxView, 0, len(txs))
	for _, t := range txs {
		av.Transactions = append(av.Transactions, txView(t))
	}
	return av, nil
}
