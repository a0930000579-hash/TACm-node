# TAC 部署與運維手冊（Deployment & Operations Guide）

涵蓋：本機快速啟動、Render 雲端部署、多節點組網、BSC 跨鏈橋實裝、
資料備份/恢復、升級流程、監控與常見故障排查。適用 tacweb（單進程全功能節點）。

---

## 1. 系統架構總覽

| 元件 | 說明 | 默認端口 |
|---|---|---|
| `tacweb` | 單進程節點：共識出塊＋RPC＋Web（區塊瀏覽/錢包/交易所/礦機/社群/DeFi） | `-web-port`＝`-rpc-port` 共用 |
| SQLite | 鏈資料＋帳戶狀態（`-data-dir` 內） | — |
| P2P | 節點間廣播/共識（`-p2p-port`，可選） | 8333 |
| eth_* JSON-RPC | MetaMask 兼容介面（同一 RPC 端口） | 同 RPC |
| BSC 中繼 | 跨鏈 mint/unlock（`-bsc-relay` 啟用） | 輪詢，無端口 |

**資料目錄（-data-dir）**：區塊/交易/帳戶/內存池＋VC/橋進度＋Wallet 記錄。

---

## 2. 本機快速啟動

```bash
# 建置
cd tacm-go
GOTOOLCHAIN=local go build -o tacweb ./cmd/web

# 啟動（Web 與 RPC 共用端口；測試用 1 秒出塊＋難度 1）
./tacweb -web-port 8080 -rpc-port 8080 -data-dir ./data \
  -block-time 1 -difficulty 1

# 驗證
curl http://127.0.0.1:8080/status          # 節點狀態
curl http://127.0.0.1:8080/api/chain/stats  # 全鏈統計
open http://127.0.0.1:8080                  # Web 入口
```

> 正式運行建議：`-block-time 2`＋`-difficulty 3`，並用 systemd / supervisor 託管。

### systemd 範例（/etc/systemd/system/tac.service）

```ini
[Unit]
Description=TAC Node
After=network.target

[Service]
WorkingDirectory=/opt/tac/tacm-go
ExecStart=/opt/tac/tacm-go/tacweb -web-port 8080 -rpc-port 8080 \
  -data-dir /opt/tac/data -block-time 2 -difficulty 3
Restart=always
RestartSec=5
User=tac

[Install]
WantedBy=multi-user.target
```

---

## 3. Render 雲端部署（免費）

1. 將壓縮檔解壓後上傳 GitHub（**zip 解壓須為 `tacm-go/` 資料夾**），repo 連到 Render。
2. 新建 **Web Service**：
   - **Build Command**：`cd tacm-go && go build -o tacweb ./cmd/web`
   - **Start Command**：`cd tacm-go && ./tacweb -web-port $PORT -rpc-port $PORT -data-dir ./data -block-time 1 -difficulty 1`
3. **已知坑**：
   - `/var/data` 無權限 → 一律用 `./data`。
   - 模板/靜態檔（含白皮書）改動後**須重建**（`//go:embed`）。
   - 瀏覽器 Service Worker 會快取舊頁/舊 JS：強刷或清站台資料。
   - Render 免費方案磁碟非持久 → **正式營運建議自架或付費盤**。

---

## 4. 多節點組網（去中心化）

```bash
# 節點 A（引導）：開 P2P
./tacweb -web-port 8080 -rpc-port 8080 -data-dir ./dataA \
  -p2p-port 8333 -seed "" -node-id nodeA

# 節點 B：以 A 為 seed
./tacweb -web-port 8081 -rpc-port 8081 -data-dir ./dataB \
  -p2p-port 8334 -seed ws://<A-IP>:8333 -node-id nodeB
```

- 所有節點**共用同一創世/鏈 ID**（同 `-data-dir` 種子或相同 config），透過 P2P 廣播交易與區塊，共識層（BFT）跨節點投票。
- 節點發現：`-seed` 指向已知節點，P2P 自動交換 peer 清單。
- 驗證：`GET /peers`、`/finality`、`/validators`。

---

## 5. BSC 跨鏈橋實裝

前置：在 BSC 部署 `TACMBSC`（mint/burn）＋`TACLockProxy`（deposit/withdraw）合約
（合約摘要與流程見 `BSC_DEPLOY.md`）。

```bash
./tacweb ... \
  -bridge -bsc-relay \
  -bsc-rpc https://bsc-testnet-rpc.publicnode.com \
  -bsc-pk <中繼私鑰 hex> \
  -bsc-chain-id 97 \
  -bsc-token 0x<TACMBSC 地址> \
  -bsc-lock-proxy 0x<TACLockProxy 地址> \
  -bsc-poll-ms 10000
```

- 用戶在 TAC 側「鎖定」（`/bridge/lock`）→ 中繼在 BSC 自動 mint TACMBSC。
- 用戶在 BSC 側「銷毀」（`/bridge/burn`）→ 中繼在 TAC 自動解鎖。
- 狀態查詢：`GET /bridge/bsc/status`；進度存於 `bsc_last_scanned`（斷點續跑，逐筆錯誤不阻塞）。

---

## 6. 資料備份與恢復

```bash
# 備份（停機或 sqlite online backup）
tar czf tac-backup-$(date +%F).tgz -C /opt/tac data/

# 恢復：解壓至新 data-dir 後重啟即可
tar xzf tac-backup-2026-10-09.tgz -C /opt/tac/
```

- 備份內容＝整包 `-data-dir`（區塊＋帳戶＋VC＋橋進度）。
- 升級前先備份；恢復後用 `/status` 核對 `block_height` 與 backup 時一致。
- 多節點環境：以**相同備份**重建，避免分叉。

---

## 7. 升級流程

1. 備份 data-dir（見上）。
2. `git pull`／覆蓋 `tacm-go/` 新版本 → `go build -o tacweb ./cmd/web`。
3. 停舊程序 → 啟新程序（systemd：`systemctl restart tac`）。
4. 冒煙：`/status` 出塊正常、`/api/chain/stats` 供應正確、任一錢包登入轉帳正常。
5. **相容規則**：交易/合約 memo 新舊格式向後相容；config 新增 flag 具默認值，舊命令列不需改。

---

## 8. 監控與故障排查

### 常用端點

| 端點 | 用途 |
|---|---|
| `GET /health` | 存活探測 |
| `GET /status` | 高度/共識/難度/內存池 |
| `GET /api/chain/stats` | 供應/流通/獎勵池 |
| `GET /peers` | P2P 對等節點 |
| `GET /finality` | 最終性狀態 |
| `GET /bridge/bsc/status` | 橋中繼狀態 |

### 常見故障表

| 現象 | 原因 | 處理 |
|---|---|---|
| `listen: address already in use` | 端口被佔 | 換端口或 `pgrep -x tacweb` 後 `kill -9 <PID>` |
| `mkdir /var/data: permission denied` | Render 無權限 | 改用 `-data-dir ./data` |
| 頁面舊版/JS 舊 | Service Worker 快取 | 清站台資料＋重建 tacweb |
| 不出塊 | 難度過高/無提案者 | 看 log；測試期降 `-difficulty 1` |
| 餘額為「1000.0」尾零 | 節點 FormatFloat 格式化 | 顯示/比較前轉數值，非錯誤 |
| 交易一直 pending | nonce 衝突/餘額不足 | 查 `/account` nonce 與餘額 |
| 帳本/卡片版面破圖 | 舊瀏覽器快取 | 強刷＋確認最新版 |

---

## 9. 安全注意

- 私鑰、`-bsc-pk` 只放伺服器環境變數/密鑰管理器，勿進程式碼或 log。
- 節點對外暴露 RPC 時，用防火牆限定來源或置於反向代理後（HTTPS）。
- 供應上限 52,003,300 TACm 由鏈上規則強制；TiUSD 最低流通 0.330 倍總供應。
- 合約交易入池前只讀模擬，gas 過小/壞合約直接拒絕。
- 定期備份＋升級前回歸 `go test ./...`（SDK 另跑 `npm test`）。
