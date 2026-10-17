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
	if len(dec.Data) != 68 || dec.Data[0] != crypto.ABISelector(SigMint)[0] {
		t.Fatalf("calldata=%x", dec.Data)
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
			(txs[i].Status == StatusPending || txs[i].Status == StatusBurning) {
			found = true
			if txs[i].SourceTxHash != "0x"+strings.Repeat("d1", 32) {
				t.Fatalf("sourceTxHash=%s", txs[i].SourceTxHash)
			}
		}
	}
	if !found {
		t.Fatal("未找到 BSC→TAC 解鎖記錄")
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
