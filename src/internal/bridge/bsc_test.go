package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tacm/internal/crypto"
)

// bscStub 為 BSC JSON-RPC 測試樁：可程式化回應各方法。
type bscStub struct {
	t          *testing.T
	chainID    string
	blockNum   string
	nonce      string
	gasPrice   string
	estimate   string
	sendRawH   string
	logs       []BSClog
	rawTxs     []string // 收到的 raw 交易（hex）
	tokenAddr  string
	lockProxy  string
	chainIDSet bool
}

func (s *bscStub) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.t.Fatal(err)
	}
	var res any
	switch req.Method {
	case "eth_chainId":
		res = s.chainID
	case "eth_blockNumber":
		res = s.blockNum
	case "eth_getTransactionCount":
		res = s.nonce
	case "eth_gasPrice":
		res = s.gasPrice
	case "eth_estimateGas":
		res = s.estimate
	case "eth_sendRawTransaction":
		raw, _ := req.Params[0].(string)
		s.rawTxs = append(s.rawTxs, raw)
		res = s.sendRawH
	case "eth_getLogs":
		res = s.logs
	default:
		s.t.Fatalf("未預期方法 %s", req.Method)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": res})
}

// startBSCStub 啟動 stub server 並回 RPC URL。
func startBSCStub(t *testing.T, s *bscStub) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	return srv.URL
}

// depositLog 組一筆 Deposit 事件 log（含 to topic）。
func depositLog(t *testing.T, tokenAddr, from, to string, amount, nonce int64, block int64, txHash string) BSClog {
	t.Helper()
	topic := "0x" + crypto.HexEncode(crypto.Keccak256([]byte(EventDeposit)))
	amtB := crypto.Uint256Bytes(big.NewInt(amount))
	nonceB := crypto.Uint256Bytes(big.NewInt(nonce))
	data := "0x" + crypto.HexEncode(append(amtB, nonceB...))
	return BSClog{
		Address:     strings.ToLower(tokenAddr),
		Topics:      []string{topic, strings.ToLower(tokenAddr), strings.ToLower(from), strings.ToLower(to)},
		Data:        data,
		BlockNumber: fmt.Sprintf("0x%x", block),
		TxHash:      txHash,
		LogIndex:    "0x0",
	}
}

func TestBSCClient(t *testing.T) {
	st := &bscStub{t: t, chainID: "0x61", blockNum: "0x10", nonce: "0x5",
		gasPrice: "0x3b9aca00", estimate: "0x5208", sendRawH: "0x" + strings.Repeat("ab", 32)}
	url := startBSCStub(t, st)
	c, err := NewBSCClient(url, 5*1000*1000*1000)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if id, err := c.ChainID(ctx); err != nil || id != 97 {
		t.Fatalf("chainID=%d err=%v", id, err)
	}
	if bn, err := c.BlockNumber(ctx); err != nil || bn != 16 {
		t.Fatalf("block=%d err=%v", bn, err)
	}
	if n, err := c.Nonce(ctx, "0x1111111111111111111111111111111111111111"); err != nil || n != 5 {
		t.Fatalf("nonce=%d err=%v", n, err)
	}
	if gp, err := c.GasPrice(ctx); err != nil || gp.Cmp(big.NewInt(1000000000)) != 0 {
		t.Fatalf("gasPrice=%v err=%v", gp, err)
	}
	if g, err := c.EstimateGas(ctx, "", "0x2222222222222222222222222222222222222222", []byte{1}); err != nil || g != 21000 {
		t.Fatalf("gas=%d err=%v", g, err)
	}
	if h, err := c.SendRaw(ctx, "0xabcd"); err != nil || h != st.sendRawH {
		t.Fatalf("sendRaw=%s err=%v", h, err)
	}
}

func TestBridgeContractMintCalldata(t *testing.T) {
	st := &bscStub{t: t, chainID: "0x61", nonce: "0x0", gasPrice: "0x1", estimate: "0x5208", sendRawH: "0xbb"}
	url := startBSCStub(t, st)
	c, err := NewBSCClient(url, 5*1000*1000*1000)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := NewBridgeContract(c, "0x"+"11"+strings.Repeat("00", 18)+"22", "0x"+"33"+strings.Repeat("00", 18)+"44")
	if err != nil {
		t.Fatal(err)
	}
	data, err := bc.MintCalldata("0x"+"55"+strings.Repeat("00", 18)+"66", big.NewInt(1500))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 68 {
		t.Fatalf("len=%d", len(data))
	}
	if sel := crypto.ABISelector(SigMint); data[0] != sel[0] || data[1] != sel[1] || data[2] != sel[2] || data[3] != sel[3] {
		t.Fatalf("selector=%x", data[:4])
	}
	if new(big.Int).SetBytes(data[36:68]).Cmp(big.NewInt(1500)) != 0 {
		t.Fatal("amount 不符")
	}
}

func TestParseDepositLogs(t *testing.T) {
	token := "0x" + "11" + strings.Repeat("00", 18) + "22"
	from := "0x" + "aa" + strings.Repeat("00", 18) + "bb"
	to := "0x" + "cc" + strings.Repeat("00", 18) + "dd"
	lg := depositLog(t, token, from, to, 12345, 9, 42, "0x"+strings.Repeat("ee", 32))
	evs, err := ParseDepositLogs([]BSClog{lg}, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("events=%d", len(evs))
	}
	ev := evs[0]
	if ev.Amount.Cmp(big.NewInt(12345)) != 0 || ev.Nonce.Cmp(big.NewInt(9)) != 0 {
		t.Fatalf("amount=%v nonce=%v", ev.Amount, ev.Nonce)
	}
	if !strings.EqualFold(ev.From, from) || !strings.EqualFold(ev.To, to) || !strings.EqualFold(ev.Token, token) {
		t.Fatalf("地址不符 from=%s to=%s token=%s", ev.From, ev.To, ev.Token)
	}
	if ev.BlockNum != 42 {
		t.Fatalf("block=%d", ev.BlockNum)
	}

	// 真實鏈上 indexed address topics 為 32 字節左填充（0x+64 hex），且事件
	// 來源合約為 LockProxy（≠token）。驗證正規化與「比對 indexed token 參數」
	// 的過濾邏輯（M150c 修復）。
	padded := func(addr string) string {
		b, err := crypto.HexDecode(strings.TrimPrefix(addr, "0x"))
		if err != nil {
			t.Fatal(err)
		}
		return "0x" + crypto.HexEncode(append(make([]byte, 12), b...))
	}
	lg2 := lg
	lg2.Address = "0x72297d2940418c154f4b0191857ad83cb33f45c9" // LockProxy，與 token 不同
	lg2.Topics = []string{lg.Topics[0], padded(token), padded(from), padded(to)}
	evs2, err := ParseDepositLogs([]BSClog{lg2}, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs2) != 1 {
		t.Fatalf("padded events=%d", len(evs2))
	}
	ev2 := evs2[0]
	if !strings.EqualFold(ev2.From, from) || !strings.EqualFold(ev2.To, to) || !strings.EqualFold(ev2.Token, token) {
		t.Fatalf("padded 地址不符 from=%s to=%s token=%s", ev2.From, ev2.To, ev2.Token)
	}
	if ev2.Amount.Cmp(big.NewInt(12345)) != 0 || ev2.Nonce.Cmp(big.NewInt(9)) != 0 {
		t.Fatalf("padded amount=%v nonce=%v", ev2.Amount, ev2.Nonce)
	}
}

func TestEthSignerAddress(t *testing.T) {
	signer, err := NewEthSigner("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	addr := signer.Address()
	if !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
		t.Fatalf("addr=%s", addr)
	}
	// 簽署一筆交易並解碼驗證 EIP-155。
	tx := &crypto.EthTx{
		Nonce:    big.NewInt(1),
		GasPrice: big.NewInt(2),
		Gas:      big.NewInt(3),
		To:       make([]byte, 20),
		Value:    big.NewInt(4),
		Data:     nil,
	}
	raw, err := signer.SignRaw(tx, 97)
	if err != nil {
		t.Fatal(err)
	}
	got, err := crypto.DecodeEthRawTx(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.V.Int64() < 35+2*97 || got.V.Int64() > 35+2*97+3 {
		t.Fatalf("v=%v", got.V)
	}
}

func TestBridgeRelayerTACtoBSC(t *testing.T) {
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

	cfg := DefaultRelayerConfig()
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

	// 建一筆 tacm→bsc 交易並確認鎖定（pending→locked）。
	src := crypto.Hash160ToAddress(crypto.Hash160([]byte("src-addr")))
	tgt := crypto.Hash160ToAddress(crypto.Hash160([]byte("tgt-addr")))
	res, err := b.LockAndMint("tacm", "bsc", src, tgt, 12.5, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLock(res.BridgeTxID, "0x"+strings.Repeat("a1", 32)); err != nil {
		t.Fatal(err)
	}
	// 另一筆非目標方向（bsc→tacm）不應被處理。
	res2, err := b.LockAndMint("bsc", "tacm", "0x1111111111111111111111111111111111111111", src, 5, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLock(res2.BridgeTxID, "0x"+strings.Repeat("a2", 32)); err != nil {
		t.Fatal(err)
	}

	if err := r.relayTACtoBSC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.rawTxs) != 1 {
		t.Fatalf("raw 交易數=%d 期望 1", len(st.rawTxs))
	}
	// 解碼 raw 驗證 mint calldata。
	dec, err := crypto.DecodeEthRawTx(mustHexBytes(t, st.rawTxs[0]))
	if err != nil {
		t.Fatal(err)
	}
	// M150b：中繼 mint 改走 TACBridge.withdraw——目標為橋合約（lockProxy）、
	// calldata 為 withdraw(string,address,uint256,uint256) selector。
	gotTo := "0x" + crypto.HexEncode(dec.To)
	if !strings.EqualFold(gotTo, st.lockProxy) {
		t.Fatalf("mint 目標應為橋合約 %s, got %s", st.lockProxy, gotTo)
	}
	if len(dec.Data) <= 4 || dec.Data[0] != crypto.ABISelector(SigWithdrawV2)[0] {
		t.Fatalf("calldata=%x（應為 withdraw selector）", dec.Data)
	}
	// 狀態應為 minted。
	got, err := b.Status(res.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusMinted && got.Status != StatusConfirmed {
		t.Fatalf("status=%s 期望 minted/confirmed", got.Status)
	}
	if got.TargetTxHash != st.sendRawH {
		t.Fatalf("targetTxHash=%s", got.TargetTxHash)
	}
}

func TestBridgeRelayerBSCtoTAC(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(filepath.Join(dir, "bridge")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	token := "0x" + "11" + strings.Repeat("00", 18) + "22"
	lockProxy := "0x" + "33" + strings.Repeat("00", 18) + "44"
	from := "0x" + "aa" + strings.Repeat("00", 18) + "bb"
	to := "0x" + "cc" + strings.Repeat("00", 18) + "dd"
	st := &bscStub{t: t, chainID: "0x61", blockNum: "0x64",
		logs:      []BSClog{depositLog(t, token, from, to, 8880000000000000000, 1, 100, "0x"+strings.Repeat("d1", 32))},
		tokenAddr: token, lockProxy: lockProxy}
	url := startBSCStub(t, st)

	cfg := DefaultRelayerConfig()
	cfg.BSCTestRPC = url
	cfg.BSCTokenAddr = token
	cfg.BSCLockProxyAddr = lockProxy
	cfg.BSCChainID = 97
	cfg.PollInterval = time.Hour
	r, err := NewRelayer(cfg, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.relayBSCtoTAC(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 橋上應有解鎖記錄（pending→burning）。
	target, err := eth0xToTacAddr(to)
	if err != nil {
		t.Fatal(err)
	}
	txs, err := b.ByAddress(target, 20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for i := range txs {
		if strings.EqualFold(txs[i].SourceChain, "bsc") &&
			txs[i].Status == StatusConfirmed {
			found = true
			if txs[i].SourceTxHash != "0x"+strings.Repeat("d1", 32) {
				t.Fatalf("sourceTxHash=%s", txs[i].SourceTxHash)
			}
		}
	}
	if !found {
		t.Fatal("未找到 BSC→TAC 自動解鎖記錄（期望 confirmed）")
	}
	// 掃描進度已推進。
	last, err := r.lastScannedBlock()
	if err != nil {
		t.Fatal(err)
	}
	if last != 100 {
		t.Fatalf("last=%d 期望 100", last)
	}
	// 第二輪應無新事件（進度已記錄，不重複建單）。
	if err := r.relayBSCtoTAC(context.Background()); err != nil {
		t.Fatal(err)
	}
	txs2, _ := b.ByAddress(target, 20)
	cnt := 0
	for i := range txs2 {
		if strings.EqualFold(txs2[i].SourceChain, "bsc") {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("BSC→TAC 記錄=%d 期望 1（不重複）", cnt)
	}
}

// helpers。

func mustHexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := crypto.HexDecode(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestBridgeRelayerBSCtoTACIdempotent M149t：Deposit 事件冪等去重——
// 同一事件二次處理（水位未推進／確認失敗的重掃）不重複建解鎖單；
// 低於最小中繼的 dust 事件首次即永久跳過，不再每輪重試。
func TestBridgeRelayerBSCtoTACIdempotent(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(filepath.Join(dir, "bridge")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	token := "0x" + "11" + strings.Repeat("00", 18) + "22"
	lockProxy := "0x" + "33" + strings.Repeat("00", 18) + "44"
	from := "0x" + "aa" + strings.Repeat("00", 18) + "bb"
	to := "0x" + "cc" + strings.Repeat("00", 18) + "dd"
	cfg := DefaultRelayerConfig()
	cfg.BSCTestRPC = "http://127.0.0.1:1" // 不連網，直接呼叫 handleDeposit
	cfg.BSCChainID = 97
	cfg.PollInterval = time.Hour
	cfg.Assets = []AssetConfig{
		{Token: "tacm", BSCTokenAddr: token, BSCLockProxyAddr: lockProxy, MinAmount: 1},
	}
	r, err := NewRelayer(cfg, b)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 事件 A：2 TACm > min 1 → 首次處理建單。
	evA := DepositEvent{
		Token: token, From: from, To: to,
		Amount: big.NewInt(2000000000000000000), Nonce: big.NewInt(1),
		TxHash: "0x" + strings.Repeat("d1", 32), BlockNum: 100, LogIndex: 0,
	}
	if err := r.handleDeposit(ctx, evA, cfg.Assets[0]); err != nil {
		t.Fatalf("第一次處理失敗: %v", err)
	}
	target, err := eth0xToTacAddr(to)
	if err != nil {
		t.Fatal(err)
	}
	txs, err := b.ByAddress(target, 20)
	if err != nil {
		t.Fatal(err)
	}
	if cnt := len(txs); cnt != 1 {
		t.Fatalf("第一次處理後記錄=%d 期望 1", cnt)
	}
	// M150d：自動解鎖——建單後應直接 burning→confirmed（不需外部手動觸發）。
	st, err := b.Status(txs[0].BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != StatusConfirmed {
		t.Fatalf("自動解鎖後狀態=%s 期望 %s", st.Status, StatusConfirmed)
	}
	if !strings.EqualFold(st.UnlockTxHash, evA.TxHash) {
		t.Fatalf("自動解鎖 hash=%s 期望 %s", st.UnlockTxHash, evA.TxHash)
	}
	// 同一事件再次處理（模擬水位未推進的重掃）：應跳過、不新增。
	if err := r.handleDeposit(ctx, evA, cfg.Assets[0]); err != nil {
		t.Fatalf("重複處理應跳過: %v", err)
	}
	txs2, err := b.ByAddress(target, 20)
	if err != nil {
		t.Fatal(err)
	}
	if cnt := len(txs2); cnt != 1 {
		t.Fatalf("重複處理後記錄=%d 期望仍 1（冪等）", cnt)
	}
	// dust 事件：0.5 TACm < min 1 → 首次即永久跳過（寫鍵），重複處理也不新增。
	evDust := DepositEvent{
		Token: token, From: from, To: to,
		Amount: big.NewInt(500000000000000000), Nonce: big.NewInt(2),
		TxHash: "0x" + strings.Repeat("d2", 32), BlockNum: 100, LogIndex: 1,
	}
	if err := r.handleDeposit(ctx, evDust, cfg.Assets[0]); err != nil {
		t.Fatalf("dust 首次處理應成功跳過: %v", err)
	}
	if err := r.handleDeposit(ctx, evDust, cfg.Assets[0]); err != nil {
		t.Fatalf("dust 重複處理應跳過: %v", err)
	}
	txs3, err := b.ByAddress(target, 20)
	if err != nil {
		t.Fatal(err)
	}
	if cnt := len(txs3); cnt != 1 {
		t.Fatalf("dust 後記錄=%d 期望仍 1", cnt)
	}
}

// TestBridgeRelayerMultiAssetTACtoBSC M149s：多資產 TAC→BSC——tiusd 交易
// 應使用 tiusd 的映射合約 mint（contractFor 選對合約、非回退 tacm）。
func TestBridgeRelayerMultiAssetTACtoBSC(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(filepath.Join(dir, "bridge")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	tacmTok := "0x" + "11" + strings.Repeat("00", 18) + "22"
	tiusdTok := "0x" + "55" + strings.Repeat("00", 18) + "66"
	lockProxy := "0x" + "33" + strings.Repeat("00", 18) + "44"
	st := &bscStub{t: t, chainID: "0x61", nonce: "0x0", gasPrice: "0x1",
		estimate: "0x5208", sendRawH: "0x" + strings.Repeat("f2", 32),
		tokenAddr: tiusdTok, lockProxy: lockProxy}
	url := startBSCStub(t, st)

	cfg := DefaultRelayerConfig()
	cfg.BSCTestRPC = url
	cfg.BSCPrivateKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.BSCChainID = 97
	cfg.PollInterval = time.Hour
	cfg.Assets = []AssetConfig{
		{Token: "tacm", BSCTokenAddr: tacmTok, BSCLockProxyAddr: lockProxy, MinAmount: 0.001},
		{Token: "tiusd", BSCTokenAddr: tiusdTok, BSCLockProxyAddr: lockProxy, MinAmount: 1},
	}
	r, err := NewRelayer(cfg, b)
	if err != nil {
		t.Fatal(err)
	}

	// 建 tiusd→bsc 交易並確認鎖定（應走 tiusd 合約 mint）。
	src := crypto.Hash160ToAddress(crypto.Hash160([]byte("src-multi")))
	tgt := crypto.Hash160ToAddress(crypto.Hash160([]byte("tgt-multi")))
	res, err := b.LockAndMint("tacm", "bsc", src, tgt, 25, "TIUSD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLock(res.BridgeTxID, "0x"+strings.Repeat("b1", 32)); err != nil {
		t.Fatal(err)
	}
	if err := r.relayTACtoBSC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.rawTxs) != 1 {
		t.Fatalf("raw 交易數=%d 期望 1（僅 tiusd 資產）", len(st.rawTxs))
	}
	// 解碼 raw：to 應為 tiusd 映射合約。
	dec, err := crypto.DecodeEthRawTx(mustHexBytes(t, st.rawTxs[0]))
	if err != nil {
		t.Fatal(err)
	}
	gotTo := "0x" + crypto.HexEncode(dec.To) // raw 20B → hex 字串
	if !strings.EqualFold(gotTo, lockProxy) {
		t.Fatalf("mint 目標應為橋合約 %s, got %s", lockProxy, gotTo)
	}
	if len(dec.Data) <= 4 || dec.Data[0] != crypto.ABISelector(SigWithdrawV2)[0] {
		t.Fatalf("calldata=%x（應為 withdraw selector）", dec.Data)
	}
}

// TestBridgeRelayerMultiAssetBSCtoTAC M149s：多資產 BSC→TAC——
// usdt 資產的 Deposit 事件應被掃到並建立解鎖單（每資產獨立掃描、互不阻斷）。
func TestBridgeRelayerMultiAssetBSCtoTAC(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(filepath.Join(dir, "bridge")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	tiusdTok := "0x" + "55" + strings.Repeat("00", 18) + "66"
	usdtTok := "0x" + "77" + strings.Repeat("00", 18) + "88"
	lockProxy := "0x" + "33" + strings.Repeat("00", 18) + "44"
	from := "0x" + "aa" + strings.Repeat("00", 18) + "bb"
	to := "0x" + "cc" + strings.Repeat("00", 18) + "dd"
	// 僅 usdt 資產有 Deposit 事件（tiusd 資產掃描應空而不阻斷）。
	st := &bscStub{t: t, chainID: "0x61", blockNum: "0x64",
		logs: []BSClog{depositLog(t, usdtTok, from, to, 1230000000000000000, 7, 100, "0x"+strings.Repeat("e1", 32))},
		tokenAddr: usdtTok, lockProxy: lockProxy}
	url := startBSCStub(t, st)

	cfg := DefaultRelayerConfig()
	cfg.BSCTestRPC = url
	cfg.BSCChainID = 97
	cfg.PollInterval = time.Hour
	cfg.Assets = []AssetConfig{
		{Token: "tiusd", BSCTokenAddr: tiusdTok, BSCLockProxyAddr: lockProxy, MinAmount: 1},
		{Token: "usdt", BSCTokenAddr: usdtTok, BSCLockProxyAddr: lockProxy, MinAmount: 1},
	}
	r, err := NewRelayer(cfg, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.relayBSCtoTAC(context.Background()); err != nil {
		t.Fatal(err)
	}
	target, err := eth0xToTacAddr(to)
	if err != nil {
		t.Fatal(err)
	}
	txs, err := b.ByAddress(target, 20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for i := range txs {
		if strings.EqualFold(txs[i].SourceChain, "bsc") &&
			txs[i].Status == StatusConfirmed {
			found = true
			if !strings.EqualFold(txs[i].Token, "usdt") {
				t.Fatalf("解鎖單 token=%s 期望 usdt", txs[i].Token)
			}
		}
	}
	if !found {
		t.Fatal("未找到 usdt 資產的 BSC→TAC 自動解鎖記錄（tiusd 空掃描不應阻斷）")
	}
}

// TestBridgeRelayerTACtoBSCMintIdempotent M149u：TAC→BSC mint 冪等——
// SendMint 已上鏈但本地記錄失敗（狀態回退 locked）時，下一輪重試
// 依 config 存的 BSC tx hash 補記錄，不再重發 mint（避免超發）。
func TestBridgeRelayerTACtoBSCMintIdempotent(t *testing.T) {
	dir := t.TempDir()
	b, err := New(DefaultConfig(filepath.Join(dir, "bridge")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })

	st := &bscStub{t: t, chainID: "0x61", nonce: "0x0", gasPrice: "0x1",
		estimate: "0x5208", sendRawH: "0x" + strings.Repeat("f9", 32),
		tokenAddr: "0x" + "11" + strings.Repeat("00", 18) + "22",
		lockProxy: "0x" + "33" + strings.Repeat("00", 18) + "44"}
	url := startBSCStub(t, st)

	cfg := DefaultRelayerConfig()
	cfg.BSCTestRPC = url
	cfg.BSCPrivateKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.BSCTokenAddr = st.tokenAddr
	cfg.BSCLockProxyAddr = st.lockProxy
	cfg.BSCChainID = 97
	cfg.PollInterval = time.Hour
	r, err := NewRelayer(cfg, b)
	if err != nil {
		t.Fatal(err)
	}

	src := crypto.Hash160ToAddress(crypto.Hash160([]byte("src-idem")))
	tgt := crypto.Hash160ToAddress(crypto.Hash160([]byte("tgt-idem")))
	res, err := b.LockAndMint("tacm", "bsc", src, tgt, 12.5, "TACM")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ConfirmLock(res.BridgeTxID, "0x"+strings.Repeat("a9", 32)); err != nil {
		t.Fatal(err)
	}
	// 第一輪：mint 上鏈＋寫 hash＋記錄。
	if err := r.relayTACtoBSC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.rawTxs) != 1 {
		t.Fatalf("第一輪 raw 交易數=%d 期望 1", len(st.rawTxs))
	}
	// 模擬「mint 已上鏈但本地記錄失敗」：狀態回退 locked（下輪會再進 relay）。
	got, err := b.store.GetBridgeTx(res.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	got.Status = StatusLocked
	if err := b.store.UpdateBridgeTx(got); err != nil {
		t.Fatal(err)
	}
	// 第二輪：config 已存 hash → 補記錄，不再發送（rawTxs 仍 1）。
	if err := r.relayTACtoBSC(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.rawTxs) != 1 {
		t.Fatalf("第二輪 raw 交易數=%d 期望仍 1（冪等不重發）", len(st.rawTxs))
	}
	got2, err := b.store.GetBridgeTx(res.BridgeTxID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != StatusMinted && got2.Status != StatusConfirmed {
		t.Fatalf("status=%s 期望 minted/confirmed", got2.Status)
	}
	if !strings.EqualFold(got2.TargetTxHash, st.sendRawH) {
		t.Fatalf("targetTxHash=%s", got2.TargetTxHash)
	}
}

// TestWithRetry M149v：RPC 讀取重試——瞬斷兩次後第三次成功即返回；
// 持續失敗在 attempts 次後回報錯誤（SendMint 絕不包此重試）。
func TestWithRetry(t *testing.T) {
	ctx := context.Background()
	// 前兩次失敗、第三次成功。
	n := 0
	if err := withRetry(ctx, 3, func() error {
		n++
		if n < 3 {
			return fmt.Errorf("boom")
		}
		return nil
	}); err != nil {
		t.Fatalf("重試後成功應回 nil: %v", err)
	}
	if n != 3 {
		t.Fatalf("呼叫次數=%d 期望 3", n)
	}
	// 持續失敗：attempts 次後回報錯誤。
	m := 0
	if err := withRetry(ctx, 2, func() error {
		m++
		return fmt.Errorf("boom")
	}); err == nil {
		t.Fatal("持續失敗應回 err")
	} else if m != 2 {
		t.Fatalf("呼叫次數=%d 期望 2", m)
	}
}
