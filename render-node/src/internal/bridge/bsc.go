package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"tacm/internal/crypto"
)

// BSC 鏈常數（BNB Smart Chain）。
const (
	BSCTestnetChainID = 97
	BSCMainnetChainID = 56
)

// 橋合約函數簽章（BSC 側 MintBurn 代幣＋LockProxy）。
const (
	// 代幣：mint（僅橋守衛）／burn（僅 LockProxy）。
	SigMint        = "mint(address,uint256)"
	SigBurn        = "burn(uint256)"
	SigBalanceOf   = "balanceOf(address)"
	SigTotalSupply = "totalSupply()"
	SigName        = "name()"
	SigSymbol      = "symbol()"
	SigDecimals    = "decimals()"

	// LockProxy：用戶存入 BSC 側代幣並指定 TAC 收款地址（燒毀並廣播 Deposit）。
	SigDeposit  = "deposit(address,uint256)"
	SigWithdraw = "withdraw(uint256)"

	// 事件。
	// Deposit(address indexed token, address indexed from, address indexed to, uint256 amount, uint256 nonce)
	EventDeposit = "Deposit(address,address,address,uint256,uint256)"
	// Withdraw(address indexed token, address indexed to, uint256 amount, uint256 nonce)
	EventWithdraw = "Withdraw(address,address,uint256,uint256)"
)

// DepositEvent 為 BSC LockProxy 的存款事件（BSC→TAC 解鎖請求）。
type DepositEvent struct {
	Token    string   // 0x 代幣地址
	From     string   // 0x 用戶地址
	To       string   // 0x TAC 收款地址（乙太坊風 20B）
	Amount   *big.Int // wei
	Nonce    *big.Int
	TxHash   string
	BlockNum int64
	LogIndex int
}

// BSClog 為 BSC eth_getLogs 回傳的 log 項。
type BSClog struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber string   `json:"blockNumber"`
	TxHash      string   `json:"transactionHash"`
	LogIndex    string   `json:"logIndex"`
}

// BSCClient 為 BSC JSON-RPC 客戶端（不引入外部鏈客戶端，純 HTTP）。
type BSCClient struct {
	rpcURL string
	hc     *http.Client
}

// NewBSCClient 建 BSC JSON-RPC 客戶端。
func NewBSCClient(rpcURL string, timeout time.Duration) (*BSCClient, error) {
	rpcURL = strings.TrimSpace(rpcURL)
	if rpcURL == "" {
		return nil, errors.New("bsc: RPC URL 為空")
	}
	return &BSCClient{
		rpcURL: rpcURL,
		hc:     &http.Client{Timeout: timeout},
	}, nil
}

type bscRPCReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type bscRPCRes struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call 執行 JSON-RPC 方法，結果解碼至 out（out 可為 nil）。
func (c *BSCClient) call(ctx context.Context, method string, params []any, out any) error {
	var id int
	if len(params) > 0 {
		id = int(time.Now().UnixNano() % 1_000_000)
	}
	body, err := json.Marshal(bscRPCReq{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("bsc: 序列化請求失敗: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("bsc: 建請求失敗: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("bsc: RPC 請求 %s 失敗: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bsc: RPC %s HTTP %d", method, resp.StatusCode)
	}
	var res bscRPCRes
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("bsc: 解析 RPC 響應失敗: %w", err)
	}
	if res.Error != nil {
		return fmt.Errorf("bsc: %s 錯誤 code=%d msg=%s", method, res.Error.Code, res.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(res.Result) == 0 || string(res.Result) == "null" {
		return fmt.Errorf("bsc: %s 回空結果", method)
	}
	if err := json.Unmarshal(res.Result, out); err != nil {
		return fmt.Errorf("bsc: 解碼 %s 結果失敗: %w", method, err)
	}
	return nil
}

// ChainID 回 BSC 鏈 ID。
func (c *BSCClient) ChainID(ctx context.Context) (int64, error) {
	var hexStr string
	if err := c.call(ctx, "eth_chainId", []any{}, &hexStr); err != nil {
		return 0, err
	}
	return parseHexInt64(hexStr)
}

// BlockNumber 回最新區塊號。
func (c *BSCClient) BlockNumber(ctx context.Context) (int64, error) {
	var hexStr string
	if err := c.call(ctx, "eth_blockNumber", []any{}, &hexStr); err != nil {
		return 0, err
	}
	return parseHexInt64(hexStr)
}

// Nonce 回地址的交易計數。
func (c *BSCClient) Nonce(ctx context.Context, addr string) (uint64, error) {
	var hexStr string
	if err := c.call(ctx, "eth_getTransactionCount", []any{addr, "pending"}, &hexStr); err != nil {
		return 0, err
	}
	n, err := parseHexInt64(hexStr)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errors.New("bsc: nonce 為負")
	}
	return uint64(n), nil
}

// GasPrice 回建議 gas 價。
func (c *BSCClient) GasPrice(ctx context.Context) (*big.Int, error) {
	var hexStr string
	if err := c.call(ctx, "eth_gasPrice", []any{}, &hexStr); err != nil {
		return nil, err
	}
	return parseHexBig(hexStr)
}

// EstimateGas 估算交易 gas。
func (c *BSCClient) EstimateGas(ctx context.Context, from, to string, data []byte) (uint64, error) {
	var hexStr string
	tx := map[string]any{
		"to":   to,
		"data": "0x" + crypto.HexEncode(data),
	}
	if from != "" {
		tx["from"] = from
	}
	if err := c.call(ctx, "eth_estimateGas", []any{tx}, &hexStr); err != nil {
		return 0, err
	}
	n, err := parseHexInt64(hexStr)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, errors.New("bsc: 估算 gas 非正")
	}
	return uint64(n), nil
}

// SendRaw 提交 raw 交易（0x hex）。
func (c *BSCClient) SendRaw(ctx context.Context, rawHex string) (string, error) {
	var h string
	if err := c.call(ctx, "eth_sendRawTransaction", []any{rawHex}, &h); err != nil {
		return "", err
	}
	if !strings.HasPrefix(h, "0x") {
		return "", errors.New("bsc: sendRaw 回非法 hash")
	}
	return h, nil
}

// ReceiptStatus 查交易收據狀態（nil = 未確認）。
func (c *BSCClient) ReceiptStatus(ctx context.Context, txHash string) (*bool, error) {
	var rec struct {
		Status string `json:"status"`
	}
	err := c.call(ctx, "eth_getTransactionReceipt", []any{txHash}, &rec)
	if err != nil {
		if strings.Contains(err.Error(), "回空結果") {
			return nil, nil
		}
		return nil, err
	}
	st, err := parseHexInt64(rec.Status)
	if err != nil {
		return nil, err
	}
	ok := st == 1
	return &ok, nil
}

// Logs 查指定範圍的日誌（topics 可 nil＝不過濾）。
func (c *BSCClient) Logs(ctx context.Context, fromBlock, toBlock int64, addresses []string, topics [][]string) ([]BSClog, error) {
	filter := map[string]any{
		"fromBlock": hexBigStr(fromBlock),
		"toBlock":   hexBigStr(toBlock),
	}
	if len(addresses) > 0 {
		filter["address"] = addresses
	}
	if len(topics) > 0 {
		filter["topics"] = topics
	}
	var logs []BSClog
	if err := c.call(ctx, "eth_getLogs", []any{filter}, &logs); err != nil {
		return nil, err
	}
	return logs, nil
}

// 合約層。

// BridgeContract 封裝 BSC 側橋合約（MintBurn 代幣＋LockProxy）。
type BridgeContract struct {
	client        *BSCClient
	tokenAddr     string // 0x
	lockProxyAddr string // 0x
}

// NewBridgeContract 建橋合約句柄。
func NewBridgeContract(client *BSCClient, tokenAddr, lockProxyAddr string) (*BridgeContract, error) {
	if client == nil {
		return nil, errors.New("bsc: 客戶端為空")
	}
	tokenAddr = normalize0x(tokenAddr)
	lockProxyAddr = normalize0x(lockProxyAddr)
	if len(tokenAddr) != 42 || len(lockProxyAddr) != 42 {
		return nil, errors.New("bsc: 合約地址須為 0x+40 hex")
	}
	return &BridgeContract{client: client, tokenAddr: tokenAddr, lockProxyAddr: lockProxyAddr}, nil
}

// MintCalldata 組代幣 mint 的 calldata（relayer TAC→BSC 用）。
func (bc *BridgeContract) MintCalldata(to string, amount *big.Int) ([]byte, error) {
	toB, err := crypto.EthAddressBytes(to)
	if err != nil {
		return nil, err
	}
	return crypto.ABIEncodeCall(crypto.ABISelector(SigMint), toB, amount)
}

// BurnCalldata 組代幣 burn 的 calldata（LockProxy 內部使用）。
func (bc *BridgeContract) BurnCalldata(amount *big.Int) ([]byte, error) {
	return crypto.ABIEncodeCall(crypto.ABISelector(SigBurn), amount)
}

// DepositCalldata 組 LockProxy.deposit 的 calldata（用戶 BSC→TAC 用）。
func (bc *BridgeContract) DepositCalldata(to string, amount *big.Int) ([]byte, error) {
	toB, err := crypto.EthAddressBytes(to)
	if err != nil {
		return nil, err
	}
	return crypto.ABIEncodeCall(crypto.ABISelector(SigDeposit), toB, amount)
}

// WithdrawCalldata 組 LockProxy.withdraw 的 calldata（用戶 TAC→BSC 取回用）。
func (bc *BridgeContract) WithdrawCalldata(nonce *big.Int) ([]byte, error) {
	return crypto.ABIEncodeCall(crypto.ABISelector(SigWithdraw), nonce)
}

// SendMint 以 relayer 私鑰簽署並發送 mint 交易，回 BSC tx hash。
// from 為 relayer 地址（0x）。
func (bc *BridgeContract) SendMint(ctx context.Context, from string, priv *EthSigner, to string, amount *big.Int, chainID int64) (string, error) {
	data, err := bc.MintCalldata(to, amount)
	if err != nil {
		return "", err
	}
	return bc.signAndSend(ctx, from, priv, bc.tokenAddr, data, chainID)
}

// signAndSend 簽署並發送一筆 BSC 交易（type 0，EIP-155）。
func (bc *BridgeContract) signAndSend(ctx context.Context, from string, priv *EthSigner, to string, data []byte, chainID int64) (string, error) {
	nonce, err := bc.client.Nonce(ctx, from)
	if err != nil {
		return "", err
	}
	gasPrice, err := bc.client.GasPrice(ctx)
	if err != nil {
		return "", err
	}
	gas, err := bc.client.EstimateGas(ctx, from, to, data)
	if err != nil {
		return "", err
	}
	toB, err := crypto.EthAddressBytes(to)
	if err != nil {
		return "", err
	}
	tx := &crypto.EthTx{
		Nonce:    new(big.Int).SetUint64(nonce),
		GasPrice: gasPrice,
		Gas:      new(big.Int).SetUint64(gas),
		To:       toB,
		Value:    big.NewInt(0),
		Data:     data,
	}
	raw, err := priv.SignRaw(tx, chainID)
	if err != nil {
		return "", err
	}
	return bc.client.SendRaw(ctx, "0x"+crypto.HexEncode(raw))
}

// ParseDepositLogs 從 logs 解析 BSC→TAC 的存款事件（topic0 匹配、解碼 indexed 參數）。
// 僅接受指定代幣地址的事件。
func ParseDepositLogs(logs []BSClog, tokenAddr string) ([]DepositEvent, error) {
	topic := "0x" + crypto.HexEncode(crypto.Keccak256([]byte(EventDeposit)))
	tokenAddr = strings.ToLower(normalize0x(tokenAddr))
	var out []DepositEvent
	for _, lg := range logs {
		if len(lg.Topics) < 4 {
			continue
		}
		if !strings.EqualFold(lg.Topics[0], topic) {
			continue
		}
		if !strings.EqualFold(normalize0x(lg.Address), tokenAddr) {
			continue
		}
		tokB, err := crypto.HexDecode(strings.TrimPrefix(lg.Topics[1], "0x"))
		if err != nil {
			return nil, fmt.Errorf("bsc: 解析 Deposit token 失敗: %w", err)
		}
		fromB, err := crypto.HexDecode(strings.TrimPrefix(lg.Topics[2], "0x"))
		if err != nil {
			return nil, fmt.Errorf("bsc: 解析 Deposit from 失敗: %w", err)
		}
		toB, err := crypto.HexDecode(strings.TrimPrefix(lg.Topics[3], "0x"))
		if err != nil {
			return nil, fmt.Errorf("bsc: 解析 Deposit to 失敗: %w", err)
		}
		dataB, err := crypto.HexDecode(strings.TrimPrefix(lg.Data, "0x"))
		if err != nil {
			return nil, fmt.Errorf("bsc: 解析 Deposit data 失敗: %w", err)
		}
		if len(dataB) != 64 {
			return nil, fmt.Errorf("bsc: Deposit data 應為 64 字節, got %d", len(dataB))
		}
		bn, err := parseHexInt64(lg.BlockNumber)
		if err != nil {
			return nil, err
		}
		idx, err := parseHexInt64(lg.LogIndex)
		if err != nil {
			idx = 0
		}
		out = append(out, DepositEvent{
			Token:    crypto.EthAddressHex(tokB),
			From:     crypto.EthAddressHex(fromB),
			To:       crypto.EthAddressHex(toB),
			Amount:   new(big.Int).SetBytes(dataB[0:32]),
			Nonce:    new(big.Int).SetBytes(dataB[32:64]),
			TxHash:   lg.TxHash,
			BlockNum: bn,
			LogIndex: int(idx),
		})
	}
	return out, nil
}

// 工具。

func normalize0x(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") {
		s = "0x" + s
	}
	return strings.ToLower(s)
}

func parseHexInt64(s string) (int64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	if s == "" {
		return 0, errors.New("bsc: 空 hex")
	}
	n := new(big.Int)
	if _, ok := n.SetString(s, 16); !ok {
		return 0, fmt.Errorf("bsc: 非法 hex %q", s)
	}
	if !n.IsInt64() {
		return 0, fmt.Errorf("bsc: hex 超出 int64 %q", s)
	}
	return n.Int64(), nil
}

func parseHexBig(s string) (*big.Int, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	n := new(big.Int)
	if _, ok := n.SetString(s, 16); !ok {
		return nil, fmt.Errorf("bsc: 非法 hex %q", s)
	}
	return n, nil
}

func hexBigStr(v int64) string {
	if v < 0 {
		v = 0
	}
	return "0x" + fmt.Sprintf("%x", v)
}

// EthSigner 包裝乙太坊風簽署（EIP-155 type 0）供 relayer/橋使用。
type EthSigner struct {
	priv *crypto.EthKey
	addr string // 0x
}

// NewEthSigner 從 hex 私鑰建簽署器，addr 為對應 0x 地址。
func NewEthSigner(privHex string) (*EthSigner, error) {
	priv, err := crypto.EthKeyFromHex(privHex)
	if err != nil {
		return nil, fmt.Errorf("bsc: 私鑰解析失敗: %w", err)
	}
	pub := priv.PubKey()
	// 0x 地址 = keccak256(uncompressed pub[1:]) 後 20 字節（乙太坊標準）。
	uncomp := pub.SerializeUncompressed()
	addr := crypto.Keccak256(uncomp[1:])[12:32]
	return &EthSigner{priv: priv, addr: crypto.EthAddressHex(addr)}, nil
}

// Address 回 0x 地址。
func (s *EthSigner) Address() string { return s.addr }

// SignRaw 以 EIP-155 簽署交易並回 RLP raw。
func (s *EthSigner) SignRaw(tx *crypto.EthTx, chainID int64) ([]byte, error) {
	return crypto.SignEthRawTx(tx, s.priv, chainID)
}
