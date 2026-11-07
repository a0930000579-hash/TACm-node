# TAC Ai 智能鏈 — 礦工節點架設教學

> 本文件教你架設 **TAC 礦工節點**（電腦版與手機版兩種），與主網對接真實出塊、參與算力瓜分。
> 節點即礦工：節點自身算力 1vCPU，啟動即註冊為默認礦機並持續出塊。

## 一、節點是什麼？

| 角色 | 說明 |
|---|---|
| 全節點（節點＋挖礦） | 完整運行鏈（出塊＋共識＋錢包＋Web），自動成為一台礦機 |
| 輕量礦工（手機） | 只運行挖礦前端（PWA），透過 RPC 連線全節點，心跳＋瓜分收益 |

兩種都能挖礦：全節點收益入節點地址；手機礦工以自己的地址註冊礦機，出塊時按算力占比入帳。

---

## 二、電腦架設全節點（推薦）

### 2.1 安裝 Go（僅需一次）
```bash
# Linux / macOS
# 下載 Go 1.23+：https://go.dev/dl/ 選 linux-amd64 / darwin-amd64 版
# 或 Ubuntu 一鍵：
sudo apt update && sudo apt install -y golang-go
# 檢查：
go version   # 需 >= 1.21
```

### 2.2 取得程式
```bash
# 解壓 tacm-go 壓縮包（zip 內為 tacm-go/ 資料夾）
cd tacm-go
# 編譯
go build -o tacweb ./cmd/web
# 檢查：
ls -la tacweb   # 應出現可執行檔
```

### 2.3 啟動節點
```bash
# 單一埠模式（Web＋RPC 同埠，最簡單）
./tacweb -data-dir ./data -web-port 8080 -rpc-port 8080 -block-time 1 -difficulty 1
```
啟動成功會看到：
```
[genesis] 創世區塊已生成
[node] 節點 node1 已啟動，地址 tx01...
[rpc] 節點 RPC: http://0.0.0.0:8080
[web] Web 區塊瀏覽: http://0.0.0.0:8080
```

### 2.4 確認挖礦
- 開啟瀏覽器 `http://你的IP:8080/mining`
- 頂部「登入」→ 註冊（可填自己的節點地址為邀請碼）
- 或直接以節點地址挖礦：挖礦頁顯示「節點地址」→ 點「開機挖礦」
- 每 1 秒出塊，挖礦頁「全鏈總覽」會持續增長

### 2.5 開放給手機挖礦（區域網）
```bash
# 綁定 0.0.0.0 即可被同網段手機存取
./tacweb -data-dir ./data -web-port 8080 -rpc-port 8080
```
（節點預設監聽 0.0.0.0，同網段手機可直接存取）
手機瀏覽器開啟 `http://電腦IP:8080` 即可當輕量礦工頁面使用。

---

## 三、手機架設輕量礦工（PWA）

手機無需安裝 App——直接開啟節點網址即為挖礦 App（可「加入主畫面」變桌面 App）。

### 3.1 步驟
1. 手機連上與電腦同一 Wi-Fi（或使用已部署的公網網址，如 Render）。
2. 瀏覽器開啟 `http://電腦IP:8080`（或部署網址）。
3. 挖礦頁 → 「登入/註冊」獲得自己的錢包地址。
4. 「開機挖礦」→ 授權瀏覽器保持前台 → 每 10 秒心跳。
5. 建議「加入主畫面」（Safari：分享→加入主畫面；Chrome：選單→安裝應用程式），以後像 App 一樣開啟。

### 3.2 手機挖礦注意事項
- 心跳需頁面在前景：**鎖定螢幕/切換 App 超過 3 分鐘＝離線**，不參與該區塊瓜分（回前台自動恢復）。
- 收益進「你的地址」錢包，可在錢包頁查看個人資產。
- 算力固定 1vCPU+2vGPU；想提升 → 分享你的地址當邀請碼，朋友註冊後你 +0.01 CPU/+0.02 GPU。

---

## 四、手機架設全節點（進階，Termux）

> Android 可用 Termux 跑真全節點（約需 512MB RAM）。

```bash
# Termux 安裝（F-Droid 商店搜尋 Termux）
pkg update && pkg install -y golang
# 上傳/下載 tacm-go 原始碼到 ~/tacm-go
cd ~/tacm-go && go build -o tacweb ./cmd/web
# 啟動（Termux 後台跑，關螢幕持續）
termux-wake-lock
./tacweb -data-dir ./data -web-port 8080 -rpc-port 8080
```
iOS 無等效終端環境，建議手機當輕量礦工、電腦/伺服器當全節點。

---

## 五、公網節點（Render 免費）

Render 免費版＝無持久碟（重啟後鏈資料重置、地址重生成），適合**體驗與測試**，不建議當正式節點。
正式營運建議：Oracle Cloud 永久免費 VPS / 自有伺服器（見 DEPLOY.md 四方案）。

---

## 六、驗證節點健康

| 項目 | 指令/網址 | 預期 |
|---|---|---|
| 出塊 | `curl http://IP:8080/status` | `block_height` 持續增加 |
| 錢包同步 | `curl http://IP:8080/api/wallet/info?address=你的地址` | `synced_block` 等於鏈頂 |
| 礦機在線 | `curl http://IP:8080/api/miners` | 你的地址 `online:true` |
| 出塊入帳 | 挖礦頁「全鏈總覽」 | 總產出隨高度增長 |
