# TAC 主網上線手冊（MAINNET GUIDE）

完全自主主網——**不依賴 BSC／以太坊／任何第三方**。TAC 是一條自己的鏈
（如同比特幣／以太坊／Solana／BSC 各自擁有自己的鏈），主網＝**你的節點公開持續運行**。

---

## 1. 主網參數（已定）

| 參數 | 值 |
|---|---|
| 鏈 ID | `tacm-mainnet-1` |
| 網路 | `mainnet` |
| 供應上限 | **52,003,300 TACm**（鏈上強制，coinbase 超過即停） |
| 穩定幣 | TiUSD（最低流通＝總供應 0.330 倍，按市值增發/銷毀） |
| 共識 | PoW 出塊（動態難度）＋BFT 最終性＋view change |
| 地址格式 | `tx01...`（secp256k1） |
| 合約 | VM（`vm:deploy:<gas>:<hex>`，gas 1..10,000,000 治理上限） |
| 預設端口 | Web/RPC 共用 `8080`、P2P 端點 `http://<IP>:8080/p2p/` |

> 主網參數由創世區塊＋節點 config 共同決定；所有節點跑同一個 `tacweb` 二進位、
> 連同一個 seed，即構成同一個主網。

---

## 2. 架設錨點節點（第一個公開節點＝主網入口）

### 需求
- VPS 1 核 / 2GB / Ubuntu 或 Debian（免費選項：Oracle Cloud 永久免費、Hetzner 便宜 VPS）
- 固定 IP（或域名＋DDNS）

### 一鍵部署（在 VPS 上執行）

```bash
# 上傳 tacm-go 壓縮檔並解壓（解壓後資料夾名為 tacm-go）
tar xzf tacm-go-m55.zip

cd tacm-go
chmod +x scripts/deploy_mainnet.sh scripts/monitor.sh
sudo ./scripts/deploy_mainnet.sh --web-port 8080 --node-id mainnet1
```

腳本會完成：`go build` → systemd 服務（開機自啟＋崩潰自動重啟）→ 防火牆開 8080。

### 手動模式（不用腳本時）

```bash
cd tacm-go
GOTOOLCHAIN=local go build -o tacweb ./cmd/web
nohup ./tacweb -web-port 8080 -rpc-port 8080 -data-dir /var/tac/data \
  -block-time 2 -difficulty 3 \
  -p2p -p2p-url http://<你的固定IP>:8080 \
  -node-id mainnet1 > /var/tac/tac.log 2>&1 &
```

**驗證錨點**：
```bash
curl http://<你的固定IP>:8080/status     # block_height 持續上升
curl http://<你的固定IP>:8080/p2p/peers   # 節點清單
```

---

## 3. 其他人／其他節點連入你的主網

三種連法（都已實作）：

| 方式 | 對方操作 |
|---|---|
| **① 架節點連入（一起出塊）** | 對方 VPS：`./tacweb -web-port 8081 -rpc-port 8081 -data-dir ./dataB -block-time 2 -difficulty 3 -p2p -p2p-url http://<對方IP>:8081 -p2p-seed http://<你的IP>:8080 -p2p-follower -node-id nodeB` |
| **② 錢包/dApp 直接連 RPC** | 對方在 TAC Web（錢包/礦機/交易所）填你的 RPC URL：`http://<你的IP>:8080`；MetaMask 填 `http://<你的IP>:8080`（eth_* 兼容） |
| **③ SDK 開發** | 開發者用 `tacclient`（Go）／`tacjs`（JS）指向 `http://<你的IP>:8080` |

> 節點連入後會自動：握手（`/p2p/hello`）→ 交換 peer 清單 → 心跳維持 →
> 經 `/p2p/block/{height}` 同步區塊 → BFT 投票。follower 不主動出塊，
> 只同步＋驗證，適合「觀測節點／交易所節點」。

### 3.1 Render 免費站改為「跟隨節點＋公開入口」（建議架構）

Render 免費版**不適合當錨點**（閒置會休眠、磁碟非持久），但非常適合當
**全網公開 Web/RPC 入口**：設定為 follower 同步錨點，別人連你的 Render 網址
就能用錢包/瀏覽器/查鏈；資料重置或休眠都無妨（喚醒後自動追高同步）。

**Render 控制台設定（Web Service → Environment/Start Command）**：

```
Build Command:   cd tacm-go && go build -o tacweb ./cmd/web
Start Command:   cd tacm-go && ./tacweb -web-port $PORT -rpc-port $PORT -data-dir ./data -block-time 1 -difficulty 1 -p2p -p2p-url https://<你的render網址> -p2p-seed http://<錨點IP>:8080 -p2p-follower
```

- `-p2p-url` 填 Render 自己的網址（`https://xxx.onrender.com`）
- `-p2p-seed` 填**錨點的 HTTP 基址**（http://錨點IP:8080）
- 重新 Deploy 後，Render 即同步錨點區塊並對外提供 Web/RPC
- 想防休眠可加外部 cron（如 cron-job.org 每 10 分鐘 ping `/health`）

**驗證**：瀏覽器開 `https://xxx.onrender.com/status`，`block_height` 應與錨點一致。

---

## 4. 手機當節點（Android）

```bash
# 1) Play 商店安裝 Termux
# 2) 安裝 Go 與工具
pkg update && pkg install golang git
# 3) 取得原始碼（或上傳 tacm-go 壓縮檔後解壓）
cd ~ && mkdir tacm && cd tacm
# （把 tacm-go-m55.zip 傳到手機 Downloads，然後：）
unzip ~/storage/downloads/tacm-go-m55.zip
cd tacm-go
# 4) 編譯（手機較慢，約 5-15 分鐘）
go build -o tacweb ./cmd/web
# 5) 以跟隨節點連入主網（或無 -p2p-seed 時作為孤立個人節點）
./tacweb -web-port 8080 -rpc-port 8080 -data-dir ./data -block-time 2 \
  -difficulty 3 -p2p -p2p-url http://<手機內網IP>:8080 \
  -p2p-seed http://<錨點IP>:8080 -p2p-follower -node-id phone1
# 6) 瀏覽器開 http://127.0.0.1:8080 即錢包/礦機介面
```

**限制（實話）**：電池耗電＋發熱＋系統可能殺後台＋儲存小——
適合**個人礦機節點／隨身觀測節點**，不建議當公共錨點。
iPhone 無法（iOS 不提供終端），改用雲端節點＋手機開 PWA。

---

## 5. 監控

```bash
# 手動監控（每 5 秒一次）
./scripts/monitor.sh http://127.0.0.1:8080

# systemd 服務狀態
systemctl status tacnode
journalctl -u tacnode -f
```

監控項目：`/health`、`/status`（高度/難度/共識）、`/api/chain/stats`（供應/流通/獎勵池）、
`/p2p/peers`（節點線上狀態）。

---

## 6. 上線 Checklist

- [ ] VPS 就緒（固定 IP、2GB 以上）
- [ ] `go build` 成功、`go test ./...` 全綠
- [ ] 錨點啟動，`/status` 高度持續上升
- [ ] 第二台機器以 `-p2p-seed` 連入，高度追上錨點
- [ ] 錢包建立帳戶、轉帳上鏈成功
- [ ] `/p2p/peers` 顯示 peer `online:true`
- [ ] 對外公布：節點 zip 下載＋RPC 地址＋seed 地址＋白皮書

---

## 7. 常見問題

| 現象 | 處理 |
|---|---|
| follower 高度不動 | 確認 `-p2p-seed` 填**錨點的 web 端口 URL**（http://IP:8080，非獨立 P2P 端口） |
| peer `online:false` | 防火牆未開 8080／seed 填錯／錨點未啟 `-p2p` |
| Render 上 `/p2p/` 404 | Render 只開單埠，啟 `-p2p -p2p-url https://<你的域名>` 且 Build/Start 用最新二進位 |
| 不出塊 | 降 `-difficulty 1`（測試）或確認機器 CPU 足以達 `-block-time` |
