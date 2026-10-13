# TAC 開發者指南（Developer Guide）

本文說明如何在 TAC Ai 智能鏈上開發 dApp：使用官方 Go SDK（`tacclient`）
簽署與提交交易、查詢節點、以及完整的節點 RPC 端點清單。

---

## 1. 快速開始

```bash
# 啟動本地節點（含 Web 與 RPC）
./tacweb -web-port 8080 -rpc-port 8080 -data-dir ./data \
  -block-time 1 -difficulty 1

# 執行 SDK 範例（連節點、查狀態、生成密鑰、轉帳）
go run ./sdk/tacclient/example -node http://127.0.0.1:8080
```

範例輸出：

```
節點 node1 | chain_id=8888 | 高度=12 | 共識=pow_bft_distributed
新地址: tx01...                    # tx0 開頭 Base58Check 地址
私鑰(請保管): 3a2b...              # 32 字節 hex
地址餘額: 1000
已提交交易: <tx_hash>（出塊後可 GET /tx/<tx_hash> 查詢）
```

---

## 2. Go SDK（sdk/tacclient）

### 2.1 密鑰

```go
import "tacm/sdk/tacclient"

// 生成新密鑰對
key, err := tacclient.GenerateKey()
addr := key.Address()                 // tx0...
privHex := key.PrivateKeyHex()        // 務必離線保管

// 從 hex 私鑰導入
key2, err := tacclient.KeyFromPrivateKeyHex(privHex)
```

### 2.2 連節點與查詢

```go
c := tacclient.NewClient("http://127.0.0.1:8080")

st, _ := c.Status()          // 節點狀態（高度/共識/供給模型）
h,  _ := c.LatestHeight()    // 鏈頂高度
bal,_ := c.Balance(addr)     // 地址餘額（string）
non,_:= c.Nonce(addr)        // 下一個可用 nonce
blk,_:= c.Block(h)           // 區塊頭
tx, _:= c.Transaction(hash)  // 已打包交易
mp, _:= c.Mempool()          // 內存池（未打包）
```

### 2.3 簽署並提交轉帳

```go
hash, err := c.Transfer(key, "tx0收款地址", "10", "0.1", "memo")
// 流程：查 nonce → 建構交易 → ECDSA 簽署（DER hex）→ POST /tx/submit → 入內存池
```

### 2.4 進階：自組交易（合約呼叫／多欄位）

```go
tx := map[string]any{
    "from": key.Address(), "to": "tx0合約地址",
    "amount": "0", "fee": "0", "nonce": nonce,
    "ts": time.Now().Unix(), "memo": "vm:call:5000000:<calldata-hex>",
    "pubkey": key.PublicKeyHex(),
}
sig, _ := crypto.SignTransaction(tx, privBytes)  // 或 tacclient 內部簽署
tx["signature"] = sig
hash, err := c.Submit(tx)
```

> 合約交易 memo 格式：`vm:deploy:<gas>:<hex>`（部署）／`vm:call:<gas>:<hex>`（呼叫）；
> 舊格式 `vm:deploy:<hex>`／`vm:call:<hex>` 向後相容。gas 上限 1..10,000,000。

---

## 3. 節點 RPC 端點清單

| 方法/路徑 | 說明 |
|---|---|
| `GET /health` | 存活檢查 |
| `GET /status` | 節點狀態（高度/共識/供給模型/難度） |
| `GET /headers?count=N` | 最近區塊頭 |
| `GET /header/{height}` | 指定高度區塊頭 |
| `GET /block/{height}` | 區塊詳情 |
| `GET /blocks?limit=N` | 區塊列表 |
| `GET /tx/{hash}` | 交易詳情（含簽名/公鑰/合約解碼） |
| `GET /account/{address}` | 帳戶（餘額/nonce/公鑰） |
| `GET /mempool` | 內存池 |
| `GET /proof/{txhash}` | 交易存在性證明（Merkle） |
| `GET /finality` / `GET /validators` / `GET /finality-proof/{height}` | 最終性/驗證人 |
| `POST /tx/submit` | 提交簽署交易（map JSON） |
| `POST /tx/batch` | 批量提交 |
| `POST /eth` | **eth_\* JSON-RPC**（MetaMask 兼容） |
| `GET /bridge/bsc/status` | BSC 跨鏈中繼狀態 |
| `GET /api/chain/stats` | 全鏈統計（供應/流通） |

### eth_\* 兼容方法

`web3_clientVersion`、`net_version`、`eth_chainId`、`eth_blockNumber`、
`eth_gasPrice`、`eth_getBalance`、`eth_getTransactionCount`、`eth_getCode`、
`eth_call`、`eth_getBlockByNumber`、`eth_sendRawTransaction`。

---

## 4. 跨鏈（TAC ↔ BSC）

- 部署 `TACMBSC`（mint/burn）＋`TACLockProxy`（deposit/withdraw）合約至 BSC
  （詳見 `BSC_DEPLOY.md`）。
- 節點啟用：`-bridge -bsc-relay -bsc-rpc <RPC> -bsc-pk <hex> -bsc-chain-id 97
  -bsc-token <0x…> -bsc-lock-proxy <0x…>`。
- 用戶經 `/bridge/lock`（TAC→BSC）與 `/bridge/burn`（BSC→TAC）發起跨鏈；
  中繼器自動在 BSC 簽名上鏈／在 TAC 建解鎖單。

---

## 5. 安全注意

- 私鑰永不離本機；SDK 不經網絡傳輸私鑰（僅節點內簽署由官方 demo 使用）。
- 交易含 nonce（防重放）與真實 ECDSA 簽名；節點入池前驗證簽名、nonce 與餘額。
- 合約交易入池前做只讀模擬，壞合約／gas 過小直接拒絕（400）。
- 供應上限 52,003,300 TACm；TiUSD 按市值增發/銷毀且保有最低流通比例。
