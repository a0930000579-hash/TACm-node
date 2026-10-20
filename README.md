# TACm Render 節點部署包（v29）

內容（預編譯二進位方案，不需在 Render 編譯源碼）：
- nodeweb   Linux amd64 靜態二進位（含 P1-① TAC-ENS 命名服務）
- Dockerfile  單階段：COPY nodeweb → 精簡執行
- render.yaml  Render Blueprint 配置（5GB 磁碟 /var/tac/data）
- README.md / README.txt

## 部署步驟（手機/電腦都行）

### 第 1 步：推到 GitHub
1. 建 GitHub repo（公開即可）：tacm-node
2. 把本資料夾內全部內容上傳到該 repo 根目錄：
   - GitHub 網頁：Add file → Upload files → 拖入 nodeweb、Dockerfile、render.yaml（單一資料夾，勿拆散）

### 第 2 步：Render 建立服務
1. 到 https://render.com 註冊（GitHub 帳號登入最快）
2. Dashboard → New → Blueprint（讀 render.yaml）→ 選 tacm-node repo
3. 服務名：tacm-anchor2（任意）

### 第 3 步：磁碟（重要）
- render.yaml 已要求 5GB 磁碟掛在 /var/tac/data
- 免費 tier 不支援磁碟 → 需升級至 Starter（約 $7/月）才保有持久鏈資料

### 第 4 步：驗證同步
- 瀏覽器開 https://你的render網址/health → 回 ok
- https://你的render網址/node-panel → 鏈高追上主網即完成（P2P 自動經 seed 同步）

## 節點角色
- Dockerfile CMD 已含 `-p2p-follower`（跟隨模式，不主動出塊、不污染主鏈）
- 主網出塊由 tac-anchor 錨點負責；Render 節點作同步/驗證節點

## 更新節點
- 新版本：GitHub repo 覆蓋 nodeweb → Render autoDeploy 自動重建
