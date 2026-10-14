# TAC Ai 智能鏈 — 運行手冊（RUNBOOK）

## 1. 從零啟動

```bash
cd tacm-go
GOTOOLCHAIN=local go build -o tacweb ./cmd/web   # 單進程：節點+RPC+Web
./tacweb -web-port 8840 -rpc-port 8841 -data-dir ./data -block-time 1 -difficulty 1
```

| 頁面 | 網址 |
|---|---|
| 區塊瀏覽器 | http://127.0.0.1:8840/ |
| 錢包 | http://127.0.0.1:8840/wallet |
| 交易所 | http://127.0.0.1:8840/exchange |
| 管理儀表板 | http://127.0.0.1:8840/dashboard |
| RPC 直連 | http://127.0.0.1:8841 |

多節點：`bash run_distributed.sh`（4 節點 BFT 網）／`bash run_vc_smoke.sh`（view-change 驗證）。

## 2. 日常操作（RPC 摘要）

| 操作 | 請求 |
|---|---|
| 新錢包 | `GET /wallet/new` |
| 鏈上轉帳 | `POST /api/wallet/transfer {from,to,asset,amount}`（TACm 2%／TiUSD 0.5%／USDT 1.25%） |
| 入金交易所 | `POST /api/exchange/deposit_from_wallet {uid,asset,amount}` |
| 提現回鏈上 | `POST /api/exchange/withdraw_to_wallet {uid,asset,amount}` |
| 掛單 | `POST /api/exchange/order {uid,market,side,type,price,qty}` |
| 閃兌 | `POST /api/exchange/flash {uid,from,to,amount}` |
| 機器人 | `POST /api/exchange/bot/start`／`stop`／`GET /api/exchange/bots` |
| TiUSD 發行/銷毀 | `POST /api/tiusd/mint|burn {to|amount,note}` |
| 獎勵池 | `GET /api/rewardpool`／`POST /api/rewardpool/topup {asset,amount}`／`withdraw {to,amount}` |
| 系統狀態 | `GET /api/status`（聚合） |
| 跨鏈橋 | `GET /bridge/chains`／`POST /bridge/lock {from_chain,to_chain,asset,amount}` |
| L2 | `POST /l2/deposit {to,asset,amount}`／`GET /l2/status` |

## 3. 冒煙驗證（每次改動/部署後）

```bash
bash run_exchange_smoke.sh   # 16
bash run_wallet_smoke.sh     # 8
bash run_m15_smoke.sh        # 17 資產互通
bash run_m16_smoke.sh        # 10 獎勵池
bash run_m17_smoke.sh        # 8 聚合/儀表板
bash run_vc_smoke.sh         # BFT 多數認證
go vet ./... && go test -race ./...
```

## 4. 故障排除

| 症狀 | 處理 |
|---|---|
| 埠被佔 | `pkill -x tacweb; pkill -x tacnode` 後重啟 |
| 冒煙掛起 | 以 `timeout 120 bash …` 執行；檢查節點 log |
| 錢包同步跳號 | 節點提示「鏈重組需重新同步」→ 以新 `-data-dir` 重啟（重組策略見 FIN_ACCEPTANCE 已知限制） |
| 出塊慢 | `-block-time 1 -difficulty 1` 為測試配置；正式網提高難度 |
| 對外連不上 | 公網部署走 `DEPLOY.md`（Render 免費）；本地僅 127.0.0.1 |

## 5. 部署與行動端

- 免費部署：`DEPLOY.md`（Render 一鍵 / Pages+隧道 / 本地）。
- Android：PWA「加到主螢幕」或 `bash pack_apk.sh`（TWA）。
- iOS：Safari「加入主畫面」。

## 6. 建議正式網參數（商業化）

- `-block-time 3 -difficulty 4`（主網）；驗證人集≥4 節點 BFT；TiUSD 作為交易/轉帳首選資產（0.5% 最低費）；獎勵池作為生態激勵（15% coinbase + 交易費結算）。
