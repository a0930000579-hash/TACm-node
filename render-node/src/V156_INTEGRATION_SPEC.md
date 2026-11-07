# TACm 智能鏈 v156 — 平行開發整合契約（所有子代理必讀）

本輪（v156）在**已完成的 v155 基礎上**新增 5 項，全部完成後「一次部署」。
主網目前仍在 **v154**（v155 未部署），故 v156 更新包＝v154→v156 全差異。

## 0. 絕對路徑與環境
- 主模組：`/home/user/Doubao/chats/38443747983290370/TACmMine/tacm-go`（Go 1.23，module 名 `tacm`，import 前綴 `tacm/internal/...`）
- 渲染節點：`/home/user/Doubao/chats/38443747983290370/TACmMine/render-node`
- 直接在上述專案樹內開發（勿另拷貝）。基線 `go build ./...` 已確認通過。
- 所有公開註釋/介面以**英文為主、中文可切換**；Go 程式碼註釋可中文（沿用現有風格）。
- 嚴格 Go 慣用：goroutine 處理併發、`context.Context`、完整 `error` 處理、不殘留 Python 思維。

## 1. v155 已存在（保留，只可優化，禁止重複開發）
`internal/dexv3`、`internal/perp`、`internal/aimarket`、`internal/node/auth.go`(SIWE)，
前端 `dexv3.html / perp.html / ai-market.html / auth.html`。
現有基礎：`internal/exchange`(中心化撮合)、`internal/rollup`、`internal/zk`、`internal/oracle`、
`internal/ai`、`internal/defi`、`internal/dex`(V2)、`internal/ens`、`internal/bridge`、`internal/vm`、`internal/wallet`、`internal/chaindb`。

## 2. 五項分工（套件名／Store 欄位／DB 已固定，勿改）
| 項 | 套件 | node 欄位 | DB 檔 | 前端頁 | 路由前綴 |
|---|---|---|---|---|---|
| A1 壓測+狀態治理 | `internal/stateprune`(+chaindb 新增檔) + `cmd/loadtest` | `pruner *stateprune.Pruner` | 治理 chain.db | 無頁面 | `GET /api/stateprune/stats` |
| A2 鏈上訂單簿現貨 | `internal/orderbook` | `obSt *orderbook.Store` | `orderbook.db` | `orderbook.html`，GET `/orderbook` | `/api/orderbook/*` |
| A3 ZK rollup 完善 | 優化 `internal/rollup`+`internal/zk` | 沿用 `n.l2`/`n.l2svc`，勿新增重複 | 沿用 | 擴充既有 `zk.html` | `/api/rollup/*`、`/api/zk/*` |
| A4 預言機多源聚合 | 優化 `internal/oracle` | 沿用 `n.oracleSvc *oracle.Oracle` | 沿用 | 擴充既有 `oracle.html` | `/api/oracle/*` |
| A5 跨鏈 AI 推理驗證 | `internal/aiverify`(+aimarket 新增 additive 檔) | `aivSt *aiverify.Store` | `aiverify.db` | `ai-verify.html`，GET `/ai-verify` | `/api/aiverify/*` |

## 3. 統一套件骨架契約（A2/A5 新套件、A1 stateprune 適用）
參考 `internal/aimarket/aimarket.go`、`internal/ens` 的 modernc.org/sqlite 寫法：
- import `_ "modernc.org/sqlite"`；SQLite 開 WAL、**單一 `*sql.DB` 連線**（SetMaxOpenConns(1)）、`sync.Mutex` 保護。
- 建構子固定簽名：`func Open(dataDir string) (*Store, error)`；`func (s *Store) Close() error`。
- DDL 用 `CREATE TABLE IF NOT EXISTS ...`；對欄位加索引；金額用 float64（對外 JSON）+ 內部必要時用整數最小單位，**與既有 dexv3/perp/aimarket 一致**（先讀它們對齊）。
- 本包只維護純狀態與持久化；實際 TACm/TiUSD 資金移轉在 `internal/node/xxx_rpc.go` 透過 `walletSvc` 完成（與 aimarket 同分工）。
- 結構需正確，可簡化但不可省略核心機制。

## 4. node 接線模式（隻能新增各自的 `xxx_rpc.go`，見第 6 點共享檔禁令）
- RPC 服務型別為 `*RPCServer`（定義於 `internal/node/rpc.go`），節點本體為 `s.node *Node`。
- handler 簽名：`func (s *RPCServer) handleXxx(w http.ResponseWriter, r *http.Request)`。
- 路由在 `NewRPCServer(n *Node)`（rpc.go 約 113 行起）內統一用
  `s.addRouteDoc("POST /api/xxx/yyy", "中文說明", s.handleXxx)` 註冊（此函式同時掛載＋建文件）。
- 資金：造金用 `n.walletSvc.Reward(addr, wallet.AssetTACm, wallet.NewAmountF(amt, wallet.AssetTACm), memo)`；
  轉帳 `_, err := s.node.walletSvc.Transfer(from,to,asset,wallet.NewAmountF(x,asset),memo)`（**回 2 值**）。
- 地址正規化：`normalizeEthAddr` 只收 `0x`；handler 需把 0x 正規化成 tx0（參考 aimarket_rpc.go / aimarket_e2e_test.go 的 aimTestAddr）。

## 5. 前端頁面契約（A2 orderbook.html、A5 ai-verify.html；A3/A4 擴充既有頁）
- 新頁放 `internal/web/templates/xxx.html`，**`{% extends "base.html" %}`**，幣安風、行動優先 App 化、全裝置自適應。
- 頁內自訂 `var T = (k, zh, en) => ...`（英文為主、中文可切換）；地址一律取 `localStorage.getItem('tac_defi_addr')`。
- 頁面 handler：`internal/web/handlers.go` 加 `func (s *Server) handleXxx(w,r){ s.render(w,"xxx.html", pongo2.Context{}) }`。
- 頁面路由：`internal/web/server.go` 加 `s.mux.HandleFunc("GET /xxx", s.handleXxx)`。
- 導航：base.html 頂部 `.navlinks`（約 69–90 行）與底部 `.bottom-nav`（約 147 行起）；新頁導航連同 i18n key 由你在 wiring block 提供，**由整合者統一插入**（避免多人改 base.html）。

## 6. ★共享檔禁令（平行階段嚴格遵守，避免互相覆蓋）
平行開發期間，**禁止編輯以下 5 個共享檔**：
`internal/node/node.go`、`internal/node/rpc.go`、`internal/web/handlers.go`、`internal/web/server.go`、`internal/web/templates/base.html`。
你可以：
- 新增你自己的套件目錄檔案、`internal/node/xxx_rpc.go`、`internal/node/xxx_e2e_test.go`、`internal/web/templates/xxx.html`（檔名各自專屬，互不重複）。
- 對「你負責優化的現有套件」（A3 rollup/zk、A4 oracle、A5 aimarket additive 檔、A1 chaindb 新增檔）**新增檔案**為主；如確需修改現有檔案，僅可最小改動且不得改變被 node.go 依賴的公開建構簽名（如 `oracle.New()`、`rollup.New(Config)`、`aimarket.Open`）。
你**不需要也不可以**跑 `go build ./...`（平行期 node 包會暫時無法編譯，屬正常）；
驗證範圍限：`go build ./internal/<你的套件>`、`go test ./internal/<你的套件>`。
A1 的 `cmd/loadtest` **不得 import `internal/node`**（避免卡在 node 包）：改以「啟動 nodeweb 子進程（`-bin` 指定二進位、臨時 datadir、空閒 port）或 `-url` 打外部節點」，對其 HTTP API 發交易；金鑰/簽名用 `internal/crypto`、`internal/wallet`。

## 7. 你必須回傳的「Wiring Block」（整合者會逐一套用，格式務必精確）
完成後在最終回覆附上，每段都要可直接貼上：
1. **node.go**：import 別名（若新增）、struct 欄位（`欄位名  *pkg.Store`）、`New(...)` 內 Open 區塊（含錯誤處理，參考 dexv3 開法）、`Close()` 內關閉區塊；A1 另給 pruner 啟動/停止區塊。
2. **rpc.go**：完整 `s.addRouteDoc(...)` 行（含 pattern、中文說明、handler 名）。
3. **web handlers.go**：頁面 handler 函式完整內容（A2/A5）。
4. **web server.go**：`s.mux.HandleFunc("GET /xxx", s.handleXxx)` 行（A2/A5）。
5. **base.html**：頂部＋底部導航的 `<a>` 行與 data-i18n key（A2/A5）。
6. 列出你**新增/修改的全部檔案絕對路徑**，以及每個公開 Struct 名稱與關鍵方法回傳值（記憶存檔點）。

## 8. 測試契約
- 套件測試：`internal/<pkg>/<pkg>_test.go`，用 `t.TempDir()` 隔離 DB，覆蓋核心機制正常＋錯誤路徑。
- node e2e：`internal/node/xxx_e2e_test.go`，`package node`，用：
  - `n, srv := newTestRPCServer(t)`（定義 eth_v53_test.go）、`waitHeight53(t, n, 2)`、
  - `postJSON(t, srv.URL+"/api/...", map[string]any{...})`、`getJSON(t, srv.URL+"/api/...")`。
  - **`getJSON` 已在 node_test.go 宣告，禁止重複宣告**；POST 用既有 postJSON。
  - 測試地址可仿 aimarket_e2e_test.go 的 aimTestAddr 自建（函式名需帶你模組前綴避免碰撞，如 `obTestAddr`/`aivTestAddr`）。
- 長測試易 Exit 143：測試要短、秒級；壓力/長運行放在 cmd/loadtest 與套件的短測試，不要塞進 e2e。
- 要求 `go test ./internal/<pkg>` 全綠；e2e 由整合者在接線後統一跑（你需保證邏輯與路由正確）。

## 9. 已知坑（逐條避開）
- wallet 常量：`wallet.AssetTACm`（**不是** AssetTACM）、`wallet.AssetTiUSD`、`wallet.AssetUSDT`。
- `walletSvc.Transfer` 回 **2 值**；沒有 `walletSvc.NewAmountF`，用 `wallet.NewAmountF(amt, asset)`。
- `normalizeEthAddr` 只收 `0x` 前綴。
- 不亂改非必要程式碼；只對錯誤做最小修正，並連帶檢查相關/串聯檔案避免衝突。
- 模組變數名避開已佔用：`es`(exchange)、`ds`(defi)、`cs2`(c2c)、`dexv3St/perpSt/aimSt`、`oracleSvc`、`l2/l2svc`。
- 冒煙 port 用 18399（勿用 18099/18199/18299）。
