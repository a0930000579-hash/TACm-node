# TAC 自主智能鏈 — 部署指南（0 → 完成）

本系統為 **Go 單二進位**（`tacweb` 同時內嵌：區塊鏈節點＋RPC＋Web＋礦機＋社群＋DeFi＋C2C，皆為可安裝 PWA）。
Python 原版全模組已 Go 化並逐版全綠交付（M0–M26），本文件提供四種部署途徑與真實限制。

---

## 0. 前置（本地建構與驗證）

```bash
# 需 Go 1.23+（`GOTOOLCHAIN=local` 強制本機工具鏈）
cd tacm-go
GOTOOLCHAIN=local go build -o tacweb ./cmd/web        # Linux 本機
GOTOOLCHAIN=local GOOS=linux GOARCH=amd64 go build -o tacweb ./cmd/web   # 跨平台 Linux
```

啟動參數（本地）：
```bash
./tacweb -web-port 8840 -rpc-port 8841 -block-time 1 -difficulty 1
# 亦可 DATA_DIR=/path ./tacweb ...（環境變數優先於 -data-dir 預設）
```

部署後必跑冒煙（每一層都是真進程全流程，FAIL 必須 = 0）：
```bash
for s in run_e2e_full.sh run_modules_rpc.sh run_exchange_smoke.sh run_m15_smoke.sh \
         run_m16_smoke.sh run_pool_smoke.sh run_miner_smoke.sh run_mining_smoke.sh \
         run_distributed_e2e.sh run_community_smoke.sh run_defi_smoke.sh run_c2c_smoke.sh; do
  bash "$s" && echo "$s OK"
done
```

---

## 1. 方案 A：Render（最快速免費上線，演示/體驗用）

> ⚠️ **真實限制（務必先讀）**：Render 免費 Web Service **沒有持久磁碟**。實例休眠/重啟（免費 tier 每 15 分鐘無流量即休眠）時，`/var/data` 會清空、鏈資料重建——**適合體驗與展示，不適合正式存證**。需要持久節點請用方案 B/C。

1. 註冊 https://render.com（GitHub 登入最快）。
2. New → **Web Service** → 連 GitHub 倉庫（或 Direct upload 上傳 `tacm-go` 目錄 zip）。
3. Build Command：`go build -o tacweb ./cmd/web`
4. Start Command：
   ```
   ./tacweb -web-port $PORT -rpc-port $PORT -data-dir /var/data -block-time 1 -difficulty 1
   ```
   Render 自動注入 `$PORT`；Web/RPC 同埠（`/api/*` 即 RPC，前端同源直連，無需隧道）。
5. Deploy → 完成。網址形如 `https://tac-chain.onrender.com`（免費冷啟動 15–30 秒屬正常）。
6. 手機開啟網址 → 加到主螢幕 → PWA 全螢幕 App。

---

## 2. 方案 B：自備伺服器/家用機 Docker（推薦正式途徑）

> 持久磁碟（`tac-data` volume）＋`restart: unless-stopped`，鏈資料跨重啟保留，為正式運行最穩方案。

```bash
# 1. 將 tacm-go 目錄（含 Dockerfile、docker-compose.yml）放到伺服器
# 2. 安裝 Docker Engine + Compose（https://docs.docker.com/engine/install/）
# 3. 一鍵啟動
docker compose up -d --build
# 4. 驗證
curl -s http://<伺服器IP>:8080/health          # {"node_id":"node1","status":"ok"}
curl -s http://<伺服器IP>:8080/status           # 區塊高度持續遞增
curl -s http://<伺服器IP>:8080/                 # 區塊瀏覽器
# 5. 日誌／更新
docker compose logs -f tac-chain                # 即時出塊日誌
docker compose down && docker compose up -d --build   # 更新版本（資料保留）
```

- 端口：Web/RPC 同 8080（`/api/*` 即 RPC）。
- 防火牆：開放 8080（或反向代理 Nginx/Caddy + HTTPS）。
- 單機單容器即完整節點；若要驗證人網路，另起多容器改 `-node-id` 與 P2P 端口（見 `run_distributed_e2e.sh` 多節點參數）。

---

## 3. 方案 C：Oracle Cloud 永久免費 VPS（0 成本持久節點）

> 雲上持久磁碟＋常駐進程＝**免費＋持久**雙滿足，適合正式上鏈。

1. 註冊 Oracle Cloud Free Tier（https://www.oracle.com/cloud/free/）：Always Free 含 2×AMD VM（1 OCPU/1GB RAM/46GB 磁碟）＋4×ARM VM。
2. 建立 VM（Ubuntu 22.04/24.04）→ 安全清單開放 TCP 8080。
3. SSH 進 VM：
   ```bash
   # 安裝 Go 1.23+
   sudo apt update && sudo apt install -y golang-go ca-certificates
   # 上傳 tacm-go 目錄（scp -r tacm-go user@ip:~/），或 git clone
   cd ~/tacm-go
   GOTOOLCHAIN=local go build -o tacweb ./cmd/web
   # 以 systemd 常駐（資料存 /var/lib/tac，跨重啟保留）
   sudo mkdir -p /var/lib/tac
   sudo useradd -r -s /bin/false tac 2>/dev/null || true
   sudo install -m 755 tacweb /usr/local/bin/tacweb
   sudo tee /etc/systemd/system/tacweb.service >/dev/null <<'EOF'
   [Unit]
   Description=TAC Autonomous Smart Chain Node
   After=network.target
   [Service]
   ExecStart=/usr/local/bin/tacweb -web-port 8080 -rpc-port 8080 -data-dir /var/lib/tac -block-time 1 -difficulty 1
   Restart=always
   User=tac
   [Install]
   WantedBy=multi-user.target
   EOF
   sudo systemctl daemon-reload && sudo systemctl enable --now tacweb
   ```
4. 驗證：`curl -s http://<VM-IP>:8080/health`；日誌 `journalctl -u tacweb -f`。

---

## 4. 方案 D：本地私有部署（最簡，演示/測試）

```bash
cd tacm-go
pkill -x tacweb; pkill -x tacnode
./tacweb -web-port 8840 -rpc-port 8841 -block-time 1 -difficulty 1
# 瀏覽器開啟：
#   http://127.0.0.1:8840/            區塊瀏覽器
#   http://127.0.0.1:8840/wallet      錢包（多資產：TACm/TiUSD/USDT）
#   http://127.0.0.1:8840/exchange    交易所（撮合/閃兌/機器人/資金池）
#   http://127.0.0.1:8840/mining      礦機（真實出塊 88/12 瓜分）
#   http://127.0.0.1:8840/community   內建社群（動態牆/市集/廣告池）
#   http://127.0.0.1:8840/defi        DeFi（流動池/借貸/IDO/收益金庫）
#   http://127.0.0.1:8840/c2c         C2C 場外交易（法幣兌加密）
#   http://127.0.0.1:8840/dashboard   管理儀表板
#   http://127.0.0.1:8840/whitepaper  白皮書（/whitepaper-en 英文）
```

---

## 5. 已驗證全量冒煙清單（每版交付皆實機執行）

| 冒煙腳本 | 覆蓋 | 斷言 |
|---|---|---|
| run_e2e_full.sh | 鏈核心 e2e（出塊/錢包/轉帳） | 23 |
| run_modules_rpc.sh | 模組 RPC 聚合 | 23 |
| run_exchange_smoke.sh | 交易所撮合/閃兌/機器人 | 16 |
| run_m15_smoke.sh | 錢包↔交易所互通與鏈上結算 | 17 |
| run_m16_smoke.sh | 獎勵池（coinbase 12%＋fee 挹注） | 10 |
| run_pool_smoke.sh | 資金池注入/topup/領用守恆 | 14 |
| run_miner_smoke.sh | 礦機 88/12 算力瓜分＋守恆 | 14 |
| run_mining_smoke.sh | 礦機 App 註冊/心跳/收益 | 13 |
| run_distributed_e2e.sh | 多節點分佈式 BFT | 6 |
| run_community_smoke.sh | 社群動態/市集/廣告閉環 | 24 |
| run_defi_smoke.sh | 流動池/借貸/IDO/Vault | 33 |
| run_c2c_smoke.sh | C2C 廣告/擔保訂單/申訴 | 26 |

全部合計 **223 項斷言**，皆真進程全流程（非 mock）。

---

## 6. 行動端（PWA）

- Android：瀏覽器「加到主螢幕」；或 `bash pack_apk.sh`（TWA APK，需 Android SDK）。
- iOS：Safari「加入主畫面」（免 App Store）。

---

## 7. 常見問題

- **免費平台無持久碟（Render/HF）**：鏈資料隨實例重啟重建——體驗用途；正式存證請用方案 B/C 的持久 volume/磁碟。
- **公網隧道（cloudflared/localtunnel/serveo 等）**：過往多輪實測全被守護軟體阻斷，不建議作為正式途徑。
- **RPC/Web 同埠**：`/api/*` 即 RPC 端點，前端同源直連，無跨域問題。
- **升級**：替換二進位重啟即可（方案 B `docker compose up -d --build`）；鏈資料（SQLite＋區塊檔）保留於 volume。
