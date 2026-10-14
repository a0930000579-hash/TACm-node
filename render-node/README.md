# TACm Render 節點部署包（M147c）

內容：
- src/   完整 tacm-go 源碼（M147c：coinbase 9/16/75 拆分＋審計修復）
- Dockerfile  兩階段建置（Go 編譯 → 精簡執行）
- render.yaml  Render Blueprint 配置

## 部署步驟（手機/電腦都行）

### 第 1 步：推到 GitHub
1. 建一個 GitHub 帳號（若無）→ 新建 repo（公開即可）：tacm-node
2. 把本資料夾內全部內容上傳到該 repo 根目錄：
   - GitHub 網頁：Add file → Upload files → 拖入 Dockerfile、render.yaml、src 資料夾
   - 或 git 指令（電腦）：git init && git add . && git commit -m "M147c" && git push

### 第 2 步：Render 建立服務
1. 到 https://render.com 註冊（GitHub 帳號登入最快）
2. Dashboard → New → Blueprint（讀 render.yaml）→ 選 tacm-node repo
3. 或 New → Web Service → 選 repo → Runtime 選 Docker
4. 服務名：tacm-anchor2（任意）

### 第 3 步：磁碟（重要）
- render.yaml 已要求 5GB 磁碟掛在 /var/tac/data
- 注意：免費 tier 不支援磁碟 → 需要升級至 Starter（約 $7/月）才保有持久鏈資料
- 若用免費 tier 且不掛磁碟：節點可跑但每次重啟會重同步（高度會歸零重來）

### 第 4 步：改 P2P 網址
- 部署完成後 Render 給你一網址：https://tacm-anchor2.onrender.com（範例）
- 需把 Dockerfile 的 CMD 中 -p2p-url 改成你自己的 Render 網址，再 Deploy
- -p2p-url = https://你的render網址（不需加 :8080，Render 443 會轉到容器 8080）

### 第 5 步：驗證同步
- 瀏覽器開 https://你的render網址/health → 回 ok
- https://你的render網址/api/chain/stats → height 追上主網即完成

## 出塊說明
- 節點為完整共識節點（-serve-dapp=false 純節點模式）
- 每次出塊獎勵 9% 自動進節點地址，私鑰在 /var/tac/data/node_key.json
- 若要多節點不互搶出塊，需要創世驗證人集（P0-1）——尚未設定前，
  主網出塊由 tac-anchor 錨點負責，Render 節點作同步/驗證節點

## 更新節點
- 有新版本時：把新 src/ 覆蓋 → git push → Render autoDeploy 自動重建

> 重要更新：Render 節點必須加 `-p2p-follower`（跟隨模式，不主動出塊）。
> 舊 render1 就是沒加這個參數才主動出塊、污染主鏈。
