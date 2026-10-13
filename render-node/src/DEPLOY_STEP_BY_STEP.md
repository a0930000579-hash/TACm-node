# TAC 主網部署 — 逐步操作單（Oracle 免費 VPS ＋ Render）

本文件是「完全自主主網」的完整逐步部署教學（架構＝**錨點 24/7 出塊**＋
**Render 免費站當公開入口**）。全程 0 元（Oracle Cloud 永久免費方案）。

總覽：**階段 A 申請 Oracle VPS → 階段 B 部署錨點 → 階段 C Render 改跟隨節點 →
階段 D 防休眠 → 階段 E 驗收與監控**。預計 1–2 小時可完成。

---

## 階段 A：申請錨點伺服器（Oracle Cloud 永久免費）

### A1 註冊
1. 手機/電腦瀏覽器開 `https://www.oracle.com/cloud/free/`
2. 點「Start for free」註冊
   - 需要：Email、密碼、姓名、國家
   - **需綁定信用卡做身分驗證，但 Always Free 方案不會扣款**
3. 驗證 Email → 完成註冊（審核通常幾分鐘～幾小時）

### A2 建立免費實例（ARM 4 核 / 24GB）
1. 登入 `https://cloud.oracle.com` → 左側 Menu → **Compute → Instances**
2. 點 **Create instance**
3. 填寫：
   - **Name**：`tac-anchor`
   - **Image**：`Canonical Ubuntu 22.04 (aarch64)`（ARM 架構才有免費）
   - **Shape**：`VM.Standard.A1.Flex`（有 **Always Free eligible** 標記）
     - 展開後調整：**OCPUs = 4、Memory = 24 GB**（免費上限）
   - **Networking**：選「Create new virtual cloud network」即可，記下之後的 **Public IP**
   - **SSH keys**：選「Paste public keys」→ 貼上你的公鑰（沒有公鑰就先在電腦/手機產生一對，見 A3）
   - **Boot volume**：預設（Always Free 200GB）
4. 點 **Create** → 等 1–2 分鐘 → 狀態變 **Running** → 記下 **Public IP**

### A3 準備 SSH 連線
**電腦（Mac / Windows）產生金鑰（若還沒有）：**
```bash
ssh-keygen -t ed25519 -f ~/.ssh/oracle_key -N ""
cat ~/.ssh/oracle_key.pub    # 把這串貼到 A2 的 SSH keys
```

**電腦連線：**
```bash
ssh -i ~/.ssh/oracle_key ubuntu@<你的Public IP>
```

**手機連線（Termius app）：** Play/App Store 裝 Termius → Hosts 新增 →
Host 填 Public IP、Username `ubuntu`、金鑰匯入（iOS 可用 A2 產出的 pem，Android 可貼文字金鑰）

> 連進去後看到 `ubuntu@tac-anchor:~$` 就成功了。

---

## 階段 B：VPS 上部署錨點（一鍵）

### B1 更新與安裝工具
```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y golang-go unzip curl
go version   # 應顯示 go1.18 以上（夠用）
```

### B2 上傳 tacm-go 壓縮檔（m56 或最新版）
**電腦：**
```bash
scp -i ~/.ssh/oracle_key tacm-go-m56.zip ubuntu@<你的Public IP>:/home/ubuntu/
```
**手機（Termius）：** 連線後點左側 File 區 → 把 zip 上傳到 `/home/ubuntu/`

### B3 解壓＋建置＋一鍵部署
```bash
cd ~
unzip tacm-go-m56.zip
cd tacm-go
sudo ./scripts/deploy_mainnet.sh --web-port 8080 --node-id mainnet1 \
  --p2p-url http://<你的Public IP>:8080
```
> 腳本自動完成：`go build` → systemd 服務（開機自啟＋崩潰自動重啟）→ ufw 開 8080。
> 看到「部署完成！」即成功。

### B4 開 Oracle 雲端防火牆（關鍵一步，很多人卡這）
Oracle 有兩層防火牆，**第二層要手動開**：
1. 回到 Oracle 控制台 → **Networking → Virtual cloud networks** → 點你的 VCN
2. 左側 **Security Lists** → 預設的那條 → **Add Ingress Rules**
3. 填：
   - Source Type：CIDR → Source CIDR：`0.0.0.0/0`
   - IP Protocol：TCP → Destination Port Range：`8080`
4. 點 **Add Ingress Rules**

### B5 驗證錨點
```bash
curl http://127.0.0.1:8080/status | python3 -m json.tool   # 本機
```
- `block_height` 應持續上升（等 5 秒再 curl 一次，數字變大）
- 從外網測（手機瀏覽器開）：`http://<你的Public IP>:8080/status`
  - 看得到 JSON＝防火牆全開成功
- 服務狀態：`systemctl status tacnode`（active running）

---

## 階段 C：Render 免費站改為「跟隨節點＋公開入口」

Render 不當錨點（免費會休眠＋磁碟非持久），改成 **follower 同步錨點**，
對全網提供 Web/RPC 入口——別人連 `https://tacm-ai-cyprto.onrender.com` 就能用。

### C1 更新 GitHub repo 內容
1. 把 m56 zip **解壓**（本機）→ 得到 `tacm-go/` 資料夾
2. 上傳/覆蓋到你的 GitHub repo `TACm-Ai_Cyprto`（用 GitHub 網頁上傳或 git push）
   - **確保 repo 根目錄有 `tacm-go/` 資料夾**（解壓後直接是資料夾，不是多包一層）

### C2 改 Build / Start Command
Render 控制台 → 你的 Service → **Settings → Build & Deploy**：
- **Build Command**：
```
cd tacm-go && go build -o tacweb ./cmd/web
```
- **Start Command**：
```
cd tacm-go && ./tacweb -web-port $PORT -rpc-port $PORT -data-dir ./data -block-time 1 -difficulty 1 -p2p -p2p-url https://tacm-ai-cyprto.onrender.com -p2p-seed http://<錨點Public IP>:8080 -p2p-follower
```

### C3 重新部署
1. 右上 **Manual Deploy → Deploy latest commit**
2. 等 Build＋Deploy（約 3–5 分鐘）
3. Logs 看到 `[node] 節點 ... 以跟隨模式啟動` ＋ `[p2p] P2P 已啟動` 即成功

### C4 驗證 Render
手機/電腦開：`https://tacm-ai-cyprto.onrender.com/status`
- `block_height` 應與錨點一致（差 0–2 塊內）
- 開 `https://tacm-ai-cyprto.onrender.com` → 錢包/礦機/交易所全部可用

---

## 階段 D：防休眠（Render 免費實例）

免費實例 15 分鐘無流量會休眠 → 用外部定時 ping 保活：
1. 註冊 `https://cron-job.org`（免費）→ My Cron Jobs → Create Cron Job
2. URL：`https://tacm-ai-cyprto.onrender.com/health`
3. Schedule：Every **10 minutes** → Create
（或 UptimeRobot 免費版相同做法）

---

## 階段 E：驗收與監控

### 上線 Checklist
- [ ] 錨點 `/status` 高度持續上升
- [ ] Oracle 外網 `http://IP:8080/status` 可開
- [ ] Render `/status` 高度與錨點一致
- [ ] Render 首頁錢包可建立帳戶、轉帳上鏈
- [ ] `https://tacm-ai-cyprto.onrender.com/p2p/peers` 顯示錨點 `online:true`
- [ ] cron-job 每 10 分鐘 ping `/health`

### 日常監控
```bash
# 錨點 VPS 上
cd ~/tacm-go && ./scripts/monitor.sh http://127.0.0.1:8080

# 任何裝置（公開入口）
curl https://tacm-ai-cyprto.onrender.com/api/chain/stats
```

---

## 疑難排解表

| 問題 | 原因 | 解法 |
|---|---|---|
| SSH 連不上 | 金鑰/安全清單 | 確認 A2 貼對公鑰、SSH 用 `ubuntu` 帳號 |
| 外網開不了 8080 | Oracle 第二層防火牆 | 見 **B4**（VCN Security List 加 Ingress 8080） |
| Render 高度不動 | seed 填錯 | `-p2p-seed` 填**錨點** `http://IP:8080`（不是 Render 自己） |
| Render 404 | 舊檔未更新 | repo 重新上傳 m56 的 `tacm-go/`，再 Manual Deploy |
| Render 一直重啟 | 端口衝突 | 確認 Start Command 用 `$PORT`（Render 指定端口） |
| 錨點不出塊 | 難度太高 | `--difficulty 1`（測試期）；正式可升 3 |
| cron 沒生效 | 沒建立 | 見階段 D；每 10 分鐘 ping `/health` |
