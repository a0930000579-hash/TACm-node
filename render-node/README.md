# TACm Render 節點部署包（v30 / v167）

內容（預編譯二進位方案，不需在 Render 編譯源碼）：
- nodeweb   Linux amd64 靜態二進位（含 P1-① TAC-ENS 命名服務）
- Dockerfile  單階段：COPY nodeweb → 精簡執行
- render.yaml  Render Blueprint 配置（5GB 磁碟 /var/tac/data）
- README.md / README.txt

## 出塊節奏
- v167 起出塊間隔為 **12 秒**（Dockerfile CMD `-block-time 12`，與主網 anchor 一致）。
- 供應模型不變：annual_decay 已依 blockTime 自動把每塊獎勵放大 4 倍，單一日曆年總發行量與 3 秒時完全相同（硬上限 52,003,300 TACm 不變）。

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

## 節點角色（v167：跟隨節點 → 驗證人輪流出塊）
- Dockerfile CMD 已改 `-block-time 12` 且 **移除 `-p2p-follower`**（明確 `-p2p-follower=false`）：節點以分布式 BFT 啟動，鏈上質押達門檻後即成為驗證人，與主網 anchor **輪流提議出塊**（round-robin）。
- 尚未質押期間：節點自動降為同步＋投票（不亂出塊，不污染主鏈）；質押生效後最遲 60 秒自動納入輪值。

### 如何成為驗證人（註冊 → 質押 → 進輪轉）
1. **自動註冊公鑰**：節點啟動時自動以自身金鑰註冊 BFT 簽名公鑰（node:reg），無需手動。
2. **質押 ≥ 1,000 TACm**：開 `/node-panel` → 質押頁面（Stake）存入至少 1,000 TACm（= `ValidatorMinStk`）。新節點首次「創節點」可領引導獎勵 1,000 TACm 直接質押。
3. **確認入集**：最遲 60 秒後動態驗證人集重建（epoch 式輪換），本節點進入 BFT validator set，開始與 anchor 輪流 proposer；權重 ∝ 總質押（自質押＋委託），每 100 TACm = 1 power。
4. **驗證狀態**：面板 /health、/node-panel 或 FinalityInfo 可看 validator 數與自身是否在輪值。
- 獎勵分配沿用現行鏈上規則（不變）：按驗證人份額出塊分潤。

## 更新節點
- 新版本：GitHub repo 覆蓋 nodeweb → Render autoDeploy 自動重建
