TACm 主網節點（Render 部署包）
================================
- nodeweb = 預編譯節點二進位（Linux amd64 靜態，Go 1.23）
- Dockerfile 直接 COPY nodeweb，不需在 Render 編譯源碼
- src/ = 節點系統源碼（供審計/自建；不含 DApp 前端）
- 啟動：同步主鏈 + 節點面板（/node-panel）+ p2p-url 自動偵測（RENDER_EXTERNAL_URL）
- v167：出塊間隔 12 秒（-block-time 12）；節點改驗證人模式（-p2p-follower=false），
  鏈上質押 ≥1,000 TACm 後自動與 anchor 輪流提議出塊；未質押期間自動同步＋投票。

若要自行編譯（在 src/ 目錄）：
  go build -o nodeweb ./cmd/nodeweb
