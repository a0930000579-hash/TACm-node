# TAC Ai 智能鏈 — 最終驗收（M19 上鏈驗收報告）

> 驗收日期：2026-10-08｜版本：tacm-go-m19｜執行：全量測試 + 冒煙回放 + 守恆審計

## 一、區塊鏈技術符合性清單（逐項驗證）

| # | 區塊鏈要件 | TAC 實作 | 驗證證據 |
|---|---|---|---|
| 1 | 密碼學簽章 | secp256k1（btcec/v2）、tx0 地址、WIF、雙 SHA-256 | crypto 包 12 測試；M0 測試向量 |
| 2 | 共識機制 | PoW（動態難度、降級重試）＋ BFT 最終性（多數認證 view-change） | bft/pos/difficulty 3 包 9 測試；VC 冒煙「3/4 多數認證接管、無分叉」PASS |
| 3 | 去中心化 | P2P 多節點（4 節點分布式網）、區塊廣播、交易池 | distributed 網正常啟動；node e2e 全綠 |
| 4 | 不可篡改 | 區塊 hash 鏈 + Merkle 根、PoW 工作量 | chaindb/crypto 測試；冒煙出塊驗證 |
| 5 | 智能合約 | EVM 相容 VM（部署/調用/儲存） | vm 包 4 測試；/contract/* RPC |
| 6 | 擴展性 | L2 Rollup（批處理、狀態樹、欺詐證明）＋ L1 提交 | rollup 包 6 測試；/l2/* RPC |
| 7 | 跨鏈互通 | 跨鏈橋（lock-mint 狀態機、多簽、SPV 輕客戶端） | bridge/spv 2 包 9 測試；/bridge/* RPC |
| 8 | 穩定幣 | TiUSD（peg 1 USDT、mint/burn、供給追踪） | wallet 測試；/api/tiusd/* |
| 9 | 交易所 | 撮合引擎（價格-時間優先）、多資產餘額、差異化手續費（TiUSD 0.5% 最優惠）、閃兌、自動交易機器人 | exchange 包 8 測試；冒煙 16/16 |
| 10 | 資產互通 | 錢包↔交易所（vault 託管、失敗回滾）、撮合費歸帳 fee 帳戶 | M15 冒煙 17/17 |
| 11 | 獎勵池 | coinbase 15% 挹注、交易所費結算入池、池提取 | M16 冒煙 10/10（5 連穩） |
| 12 | 管理面 | 聚合 RPC + 儀表板 | M17 冒煙 8/8 |
| 13 | 行動端/部署 | PWA（manifest+SW）、APK/IPA 打包腳本、免費部署指南 | M18 驗證 |

## 二、驗證結果彙總（2026-10-08 回放）

| 套件 | 結果 |
|---|---|
| 全項目 `go vet ./...` | 通過 |
| 全項目 `go test -count=1 -race ./...` | 14 包全綠（含 node e2e 75-78s） |
| run_exchange_smoke.sh | **16/16** |
| run_wallet_smoke.sh | **8/8** |
| run_m15_smoke.sh（資產互通） | **17/17** |
| run_m16_smoke.sh（獎勵池） | **10/10 ×5 連穩**（守恆證據＝提取額全額到帳 U1，池至多扣 w+fee） |
| run_m17_smoke.sh（聚合/儀表板） | **8/8** |
| run_vc_smoke.sh（BFT view-change） | **PASS**（多數認證接管、無分叉） |
| run_distributed.sh（4 節點網） | 啟動正常（交互式） |

**合計冒煙 PASS = 59 + BFT 專項 PASS；單元/整合測試 14 包全綠。**

## 三、守恆審計（資產不憑空增減）

- 錢包帳本：Transfer 拆「轉出/轉入/fee」三筆；coinbase 拆「提議者 85% / 獎勵池 15%」；TiUSD 供給 = 累計 mint − burn（專表）。
- 交易所：鎖定/解鎖/消耗對稱（M14 修「雙重解鎖」、M15 修「maker 完全成交 unlock 憑空複製」→ consumeLocked）。
- 互通：入金＝鏈上扣款＋ex 入帳（失敗回滾）；提現反向；撮合費集中 fee 帳戶可查可結算（topup→獎勵池）。
- 驗證：M15/M16 冒煙以整數 wei 斷言餘額（4.9 / 79.9 / 1.99 / 0.01 fee / 提取全額到帳）。

## 四、已知限制（誠實揭露，非隱藏）

1. 錢包提現為站內轉帳至外部地址帳戶（未廣播跨節點原始交易；節點同帳本語義下成立）。
2. 鏈重組恢復策略未自動化（ApplyBlock 對跳號報錯，重組需重同步）。
3. 公網測試連結受限：本環境出網隧道被安全策略阻斷；部署後（DEPLOY.md/Render）即有對外連結。
4. APK 需 Android SDK 環境（腳本自動偵測並給步驟）；PWA 免 SDK 即可安裝。

## 五、結論

**整套系統符合區塊鏈技術要件（密碼學/共識/去中心化/不可篡改/智能合約/擴展/互通），並具備商業價值閉環（錢包→交易所→獎勵池→管理面→行動端）。驗收通過。**

---

## 六、M20 補充驗證與補缺（全套系統實機跑一遍後）

### 6.1 本輪實機驗證抓到並修復的真實 bug（含 3 個既有 + 1 個新發現）

| # | 缺陷 | 影響 | 修復 |
|---|---|---|---|
| 4 | 撮合層無「自我交易」防護（wash trade） | 同一帳戶的對側交叉訂單會自行撮合（U2 buy 500 與自身剩餘 ask 成交 0.5、鎖定異常），可洗交易製造假量 | PlaceOrder 下單前掃同 uid 對側簿交叉單→拒單（「禁止自我交易」）；matchTaker 對「本可成交」的自身 maker 移除跳過（市價/閃兌保底）。新增 TestSelfTradeGuard 單元測試 |
| 3 | bridge `GetPendingBridgeTxs` 單連接自死鎖（SetMaxOpenConns(1) 內再開 Query） | lock 產生 pending 記錄即觸發、bridge/stats 等卡死 | 先收集 id 關閉 rows，再逐筆查詢（已於前輪修復並複驗） |
| 2 | `divPrice` 縮放量綱錯誤（quoteDec vs baseDec） | 賣 quote 買 base 閃兌路徑少 10^(baseDec−quoteDec) | 改 baseDec 並統一兩處（已於前輪修復並複驗） |
| 1 | `FlashSwap` 不撮合 maker 守恆漏洞（直接 debit/credit） | maker 資產憑空消失、閃兌所得憑空產生 | 重寫為真實吃簿市價單（maker 完整結算＋費入 fee 帳戶）（已於前輪修復並複驗） |

### 6.2 新增能力（用戶要求「含挖礦 App 與社群功能」的補缺）

- **挖礦中心頁 `GET /mining`**（internal/web/templates/mining.html）：節點識別／出塊統計／動態難度／coinbase 分潤（85/15）／獎勵池餘額（share_bp=1500）／驗證人集（BFT/PoS）／挖礦機制說明；實時數據直接讀節點 RPC（?rpc= 可指定）。
- **頂欄社群膠囊**（base.html + 聚合 `/api/status` 新增 `community` 欄位）：TAC_COMMUNITY_TELEGRAM / X / DISCORD / SITE 環境變數啟用（未配置顯示「社群陸續開放」）。
- **tacweb 新旗標 `-l2 / -bridge / -bridge-guardians`**：單進程即可全套啟用 L2 與跨鏈橋。
- **`cmd/web` 啟動順序修正**：`n.Start()` 先於 Web/RPC listener，避免「RPC 可達但尚未出塊」（聚合 block_height=0）。

### 6.3 本輪完整實機驗證結果（全部真進程、真 RPC）

| 驗證層 | 結果 |
|---|---|
| `go vet ./...` | 全綠 |
| `go test -race ./...`（14 包） | 全綠（含新增 TestSelfTradeGuard） |
| `run_e2e_full.sh`（出塊→鏈上轉帳→TiUSD→入金→撮合→閃兌→提現→獎勵池→聚合→儀表板+PWA） | PASS=23 / FAIL=0 |
| `run_modules_rpc.sh`（鏈/VM 合約預算/L2/橋 lock 與 stats/錢包/TiUSD/獎勵池/聚合/Web 全頁/PWA） | PASS=23 / FAIL=0 |
| `run_distributed_e2e.sh`（4 節點跨進程 BFT） | PASS=6 / FAIL=0 |
| `run_exchange_smoke.sh`（入金→撮合→市價→閃兌→機器人→撤單） | PASS=16 / FAIL=0 |
| `run_wallet_smoke.sh` / `run_m15_smoke.sh` / `run_m16_smoke.sh` / `run_m17_smoke.sh` / VC 冒煙 | 8 / 17 / 10 / 8 / BFT PASS |

### 6.4 驗證腳本健壯性修正（只改腳本，不改系統碼）

- run_exchange_smoke.sh：閃兌深度改用第三方 uid（防自我交易後同 uid 雙側不可共存）；撤單對象改 U3 剩餘買單。
- run_modules_rpc.sh：移除 `set -e`；重複 POST `/wallet/new`（該路由為 GET）移除；等待出塊後再斷言聚合；validators 取 `address` 欄位。
- run_e2e_full.sh / run_distributed_e2e.sh：前輪修正後維持全綠。

---

## 7. M21 礦機／資金池／社群（三點理解對齊，Go 重寫收尾）

### 7.1 完成內容

| 模組 | 對齊原本方式 | Go 實現 |
|---|---|---|
| **M21-A 礦機** | 礦機以地址註冊（VCPU/VGPU 算力）→ 週期心跳 → 出塊補貼扣除 12% 獎勵池份額後，其餘 88% 按在線礦工算力占比瓜分 | `internal/wallet/miner.go`（miners 表＋RegisterMiner/TickMiner/StopMiner/OnlineMinerSplits/DistributeByHashrate，末位補齊守恆）；`internal/wallet/sync.go`（PoolShareBps 1500→**1200**、coinbase 改 12% 入池＋88% 按算力分，無在線礦工 fallback 全歸提議者）；`internal/node/miner_rpc.go`（register/tick/stop、GET /api/miners、/api/miner/earnings）；節點啟動自動註冊自身為默認礦機（1 vCPU） |
| **M21-B 資金池** | 獎勵池照原本邏輯作為交易所資金用途（balance / total_injected / total_claimed＋管理視圖） | `internal/wallet/pool.go`（多資產聚合，修 nil 指標 panic）；`GET /api/exchange/pool`（coinbase 12%＋fee topup＋withdraw 領用，injected−claimed==balance 全程守恆）；交易所頁新增「資金池」tab |
| **M21-C 社群** | 內建 Facebook 風社群：動態牆／按讚／留言／市集（TACM 計價）／廣告池／快捷入金交易所 | `internal/community/community.go`（posts/comments/likes/ads 表＋CRUD）；13 個 `/api/community/*` 路由（含 buy-intent、ads 付款建議 pay_to=reward_pool、transfer/exchange）；`GET /community` 頁（FB 藍白風、行動優先 App 化、桌面三欄） |

### 7.2 本次修正的 bug

- `PoolSummary` 對空餘額字串 `SetString` 失敗回傳 nil `*big.Int` → `Add` panic（/api/exchange/pool 空響應）→ `stBig()` 統一解析，空值歸 0。
- `/api/community/post/{id}/like|comments` 的 id 解析在 Go 1.22 mux `{id}` 下改用 `PathValue("id")`（原取路徑最後一段會拿到 "like"）。

### 7.3 全部實機驗證（真進程、真 RPC、真區塊）

| 驗證層 | 結果 |
|---|---|
| `go vet ./...` | 全綠 |
| `go test -count=1 ./...`（14 包） | 全綠 |
| `run_e2e_full.sh` | PASS=23 / FAIL=0 |
| `run_modules_rpc.sh` | PASS=23 / FAIL=0 |
| `run_exchange_smoke.sh` / `run_wallet_smoke.sh` | 16 / 8 / 0 |
| `run_m15` / `run_m16` / `run_m17` / VC | 17 / 10 / 8 / 全綠 |
| `run_distributed_e2e.sh`（4 節點，88% 四等分瓜分） | PASS=6 / FAIL=0 |
| `run_mining_smoke.sh`（節點礦工 88%＋池 12%＋社群膠囊） | PASS=13 / FAIL=0 |
| `run_miner_smoke.sh`（節點 1vCPU＋m2 2vCPU → 88% 按 1:2 瓜分、總額守恆零流失、earnings API 一致） | PASS=14 / FAIL=0 |
| `run_pool_smoke.sh`（coinbase 注入→撮合造 fee→topup→withdraw→injected−claimed==balance） | PASS=14 / FAIL=0 |
| `run_community_smoke.sh`（發文/按讚冪等/留言/市集 buy-intent/廣告付款建議/快捷入金交易所/統計/頁面渲染） | PASS=16 / FAIL=0 |

---

## 8. M23 社群市集一鍵購買閉環（鏈上付款）

### 8.1 完成內容

| 項目 | 說明 |
|---|---|
| `POST /api/community/market/{id}/buy` | 買家一鍵購買：直接鏈上轉帳 TACM→賣家（memo=market#{id}:buyer:{addr}）；**鏈上費（2%）自動內部移轉入資金池**（InternalTransfer 免二次費）；自買防護；餘額不足回明確錯誤 |
| `GET .../buy-intent` | 保留查詢語義＋新增 `fee_tacm` 預估（FeeOf 精確計算，big.Float 轉浮點） |
| `wallet.InternalTransfer` | 新方法：資金池/費用結算等系統內部移轉，不收取手續費（不對用戶暴露） |
| community.html | 市集購買改為確認底單（賣家/金額/鏈上費 2%/合計）＋「確認付款」一鍵轉帳＋結果顯示（防重複提交） |

### 8.2 本次修正

- buy-intent 原斷言/合計與鏈上費顯示統一（C 扣 5.1=商品 5＋費 0.1、賣家收 5、池 +0.1）。
- 費用結算初版誤用 Transfer（二次扣費致 fee 帳戶不足）→ 改 InternalTransfer 內部移轉。

### 8.3 實機驗證

| 驗證層 | 結果 |
|---|---|
| `run_community_smoke.sh`（含市集一鍵購買閉環守恆：C 餘 4.9、賣家 +5、資金池 +0.1） | PASS=18 / FAIL=0 |
| `go vet ./...` ＋ `go test -count=1 ./...`（wallet/node 等） | 全綠 |
| e2e 23 / exchange 16 / m15 17 / m16 10 / pool 14 / miner 14 / mining 13 / modules 23 | 全 FAIL=0 |

---

## 9. M24 市集成交狀態＋廣告付款閉環

### 9.1 完成內容

| 項目 | 說明 |
|---|---|
| 市集成交狀態 | posts 表加 `sold`/`buyer`（PRAGMA 檢查＋ALTER 相容遷移）；`MarkSold` 原子成交（WHERE sold=0，RowsAffected 判重）；買家付款失敗 `UnmarkSold` 回滾釋放；市集列表/feed 回傳 `sold`/`buyer` |
| 防重複購買 | 第二買家購買已售商品 → 409「商品已售出」（前端顯示「已售出」＋禁用按鈕） |
| 廣告付款閉環 | `ads/create` 改預設 `pending`（待付款）；新增 `POST /api/community/ads/{id}/pay`：廣告主一鍵付款（預算全額內部移轉入資金池、免鏈上費促銷）→ `active`（投放中）；非廣告主 → 403；重複付款 → 409 |
| 前端 | 市集「已售出」標籤＋禁用；廣告池「待付款／投放中」＋「💳 付款投放」按鈕（僅廣告主可見） |

### 9.2 實機驗證

| 驗證層 | 結果 |
|---|---|
| `run_community_smoke.sh`（新增防重複購買、sold 標記、廣告付款、403 防護、池 +3 守恆） | PASS=24 / FAIL=0 |
| `go vet ./...` ＋ 14 包 `go test -count=1` | 全綠 |
| e2e 23 / modules 23 / exchange 16 / m15 17 / m16 10 / pool 14 / mining 13 / distributed 6 | 全 FAIL=0（miner 14/14 重跑全綠，首次為出塊時序 flaky 非程式錯誤） |

---

## 10. M25-A DeFi 模組搬運（流動性挖礦＋借貸市場）

### 10.1 完成內容

| 項目 | 說明 |
|---|---|
| internal/defi | SQLite 資料庫（pools/lp_positions/lending_markets/deposits/loans）＋seed 3 池與 3 借貸市場（照 Python _seed_default_data） |
| 流動性挖礦 | AddLiquidity（LP=sqrt(a0*a1)／比例式 min、頭寸合併）、RemoveLiquidity（比例贖回＋頭寸≤1e-6 刪除）、ClaimLPReward（reward_per_day×占比×天數、last_reward_at 更新） |
| 借貸市場 | Deposit/Withdraw（合併存款、餘額檢查）、Borrow（抵押值=數量簡化、max=抵押×collateral_factor、清算價=抵押×threshold/借款）、RepayLoan（10% 年息、全額→repaid＋抵押釋放、部分→減本金） |
| 真實資產進出 | 入池/存款/抵押/還款走 wallet.Transfer（鏈上費 2% 入資金池）；獎勵/借款走 Reward 或池內 InternalTransfer（免二次費）；池帳戶 seed 注資（Reward 鑄造、僅空帳戶執行、冪等） |
| API＋頁面 | 12 個 /api/defi/* 路由＋/defi 幣安風 DeFi 中心（流動池 tab＋借貸 tab，App 化） |
| 修正 | normalizeAsset 正規化資產名（ToUpper 破壞 TACm 首字母 bug）；seed pair 統一 TIUSD；退費守恆（轉帳失敗 InternalTransfer 回滾） |

### 10.2 實機驗證

| 驗證層 | 結果 |
|---|---|
| `run_defi_smoke.sh`（seed/添加/領獎/移除/存/取/借/超額拒/還款/抵押釋放/USDT 守恆/頁面） | PASS=21 / FAIL=0 |
| `internal/defi` 單元測試（seed/LP 守恆/獎勵重領拒/超額借拒/超額取款拒） | ok |
| `go vet ./...` ＋ 15 包 `go test -count=1` | 全綠 |
| 10 層冒煙回歸（e2e 23/modules 23/exchange 16/m15 17/m16 10/pool 14/miner 14/mining 13/distributed 6/community 24） | 全 FAIL=0 |

---

## 11. M25-B IDO 發行平台＋收益聚合器（Vault）

### 11.1 完成內容

| 項目 | 說明 |
|---|---|
| internal/defi 擴充 | 新增 4 表（defi_ido_projects／defi_ido_subscriptions／defi_vaults／defi_vault_positions）＋IDO 2 專案（TACG 0.5 USDT 目標 5 萬／TACA 1.0 目標 10 萬）＋Vault 3 金庫（TACM 18% min100／USDT 12% min50／TiUSD 8% min10） |
| IDO 規則（照 Python） | 時間窗內才可認購、min/max 上下限、一人一項目、tokens=amount/token_price、total_raised/participants 記帳；claim 需 project.status=completed 且防重複領取 |
| Vault 規則（照 Python） | min_deposit 檢查、首存 shares=amount 否則比例式、withdraw 贖回=shares×total/total_shares、≤1e-6 刪頭寸、compound 間隔 ≥3600s（env 可覆蓋）＋yield=total×apr×占比×年化、復投同額增份額 |
| 資產真實進出 | IDO 認購＝用戶→IDO 資金池（鏈上費 2% 入資金池）；認購失敗自動退費；Vault 本金＝用戶→金庫帳戶；贖回走池內免二次費；復投收益＝鏈上 Reward 鑄造入金庫帳戶（與流動池獎勵同機制） |
| 修正 | 全額取出後頭寸刪除致 nil 指標 panic（先取 pos 再取款）；IDO seed 時間窗 env 覆蓋（測試可測）；miner 收益斷言語義（累計 vs 餘額） |
| 前端 | /defi 新增「🚀 IDO 發行」「🏆 收益金庫」雙 tab（App 化） |

### 11.2 實機驗證

| 驗證層 | 結果 |
|---|---|
| `run_defi_smoke.sh`（IDO seed/認購 100 枚/重複拒/min_buy 拒/未完成領取拒；Vault seed/存入份額/頭寸/復投間隔拒/取出贖回/頭寸清空；原 21 斷言） | PASS=33 / FAIL=0 |
| `internal/defi` 單元測試（7 測試：seed/LP 守恆/獎勵重領拒/借貸規則/IDO 認購領取/重複拒/募集記帳/Vault 合併頭寸/復投/贖回守恆） | ok |
| `go vet ./...` ＋ 15 包 `go test -count=1` | 全綠 |
| 11 層冒煙回歸（e2e 23/modules 23/exchange 16/m15 17/m16 10/pool 14/**miner 14**/mining 13/distributed 6/community 24/**defi 33**） | 全 FAIL=0 |

---

## 12. M26 C2C 場外交易（法幣兌加密貨幣）

### 12.1 完成內容

| 項目 | 說明 |
|---|---|
| internal/c2c 新包 | SQLite（c2c_ads/c2c_orders）＋Ad/Order Store；支援 USDT/TiUSD/TACM × TWD/CNY/USD/HKD/JPY × 6 種支付方式（照 Python） |
| 廣告 | create_ad 驗證（side/asset/fiat/price/min≤max/支付方式）；list 過濾（asset+fiat+side、價格升序）；my_ads/ad-status（僅廣告主） |
| 訂單狀態機 | pending→paid（買家確認）→completed（賣家放行）；pending 可取消；pending/paid 可申訴（照 Python） |
| 擔保（Go 改進） | sell 廣告下單即鏈上凍結賣家幣入 `c2c_escrow`（含鏈上費）；放行 escrow→買家（免二次費）；取消 escrow→賣家；申訴單擔保保留 |
| buy 廣告 | 下單無凍結；放行時賣方（廣告主）向買家支付（鏈上費） |
| 修正 | `dispute_reason` NULL→string Scan 錯誤（COALESCE）；自己交易用例；守恆斷言改 escrow 擔保保留＋精確扣費（coinbase 干擾剔除） |
| 前端 | /c2c 幣安風頁面（買/賣 tab、廣告列表、發佈廣告、我的廣告、我的訂單含步驟條與申訴） |

### 12.2 實機驗證

| 驗證層 | 結果 |
|---|---|
| `run_c2c_smoke.sh`（廣告創建/非法拒/過濾、下單凍結 10.2、escrow ≥10、超額拒、自己交易拒、確認/非買家拒、放行收款、escrow 清空、completed、完成數+1、buy 單、取消/二次拒、申訴/已完成拒、escrow 申訴保留 5、頁面） | PASS=26 / FAIL=0 |
| `internal/c2c` 單元測試（廣告驗證/權限/下單/狀態機/取消/申訴） | ok |
| `go vet ./...` ＋ 16 包 `go test -count=1` | 全綠 |
| 12 層冒煙回歸（e2e 23/modules 23/exchange 16/m15 17/m16 10/pool 14/miner 14/mining 13/distributed 6/community 24/defi 33/**c2c 26**） | 全 FAIL=0 |

---

## 13. M27 部署交付（Docker/Oracle/Render 四方案＋真實限制）

### 13.1 完成內容

| 項目 | 說明 |
|---|---|
| `DATA_DIR` 環境變數 | cmd/web 支援 env 優先於 -data-dir 預設（容器/平台標準，已實機驗證） |
| Dockerfile | 多階段：golang:1.23-alpine 構建 → alpine:3.20 執行（非 root、~45MB）；Render 注入 $PORT 即同埠 RPC/Web |
| docker-compose.yml | 自備伺服器一鍵 `docker compose up -d --build`；`tac-data` 持久 volume＋restart |
| DEPLOY.md 全面重寫 | 0→完成四方案（Render 快速免費／Docker 正式／Oracle 永久免費 VPS 持久／本地）；M21–M26 冒煙 12 層 223 項斷言清單；行動端；FAQ |
| 真實限制揭露 | Render/HF 免費 tier 無持久磁碟（重啟鏈重建，體驗用）；公網隧道被守護軟體阻斷不建議；正式存證用 Docker volume 或 Oracle 持久磁碟 |

### 13.2 驗證

| 驗證層 | 結果 |
|---|---|
| DATA_DIR env 實機 | 節點啟動後資料落指定目錄（chain.db/c2c.db/community.db 等），health ok |
| go vet ./... ＋ go build | 全綠 |
| 部署後冒煙清單 | 12 層 223 項斷言全 FAIL=0（M26 版已驗） |

---

## 14. M28 同埠合併修復（Render 部署 404 根因）

### 14.1 根因

DEPLOY.md 宣稱「RPC 與 Web 同埠（/api/* 即 RPC 同源直連）」，但程式無同埠合併邏輯：
RPC 與 Web 各自 `ListenAndServe` 同埠 → Web 綁定失敗（address already in use）→ 僅 RPC 存活 → 首頁 404。

### 14.2 修復

| 檔案 | 內容 |
|---|---|
| internal/node/rpc.go | 新增 `route{pattern,h}`＋`addRoute`（註冊並記錄）＋`MountInto(mux)`（回放路由至外部 mux；跳過 `/block/{height}`、`/tx/{txhash}` 避免與 Web 語義重複的 pattern 衝突 panic）；117+ 條路由全部改走 addRoute |
| cmd/web/main.go | `-web-port == -rpc-port` 時：RPC 路由掛載進 Web mux、單一監聽（`[web+rpc] 同埠合併模式`）；分開埠維持原行為；Shutdown 條件化 |

### 14.3 驗證

| 驗證層 | 結果 |
|---|---|
| 同埠實機（`-web-port 8840 -rpc-port 8840`） | health/status/首頁 200/wallet/defi/c2c 全通、無 bind 衝突（Render 失敗場景再現並修復） |
| 分開埠回歸 | m17 8/8、exchange 16/16、c2c 26/26 全綠 |
| go vet ./... ＋ 16 包 go test | 全綠 |

---

## 15. M29 供應上限＋會員系統（線上排查修復）

### 15.1 線上排查（tacm-ai-cyprto.onrender.com）

| 檢查項 | 結果 |
|---|---|
| /health、/status | ok；block_height 313（1 塊/秒正常出塊） |
| 首頁/wallet/exchange | 200（桌面版面正常，資產顯示 #435） |
| **總供應上限** | **Go 版漏搬 Python `TACM_MAX_SUPPLY=52003300` 年衰減模型 → 可無限增發** |
| **會員功能** | **Go 版漏搬 Python `/api/auth/*`（register/login/logout＋users 表＋PBKDF2＋session cookie）** |

### 15.2 供應模型（internal/chaindb/emission.go）

- `TACM_MAX_SUPPLY=52003300`（Render 加環境變數即啟用）＋`TACM_EMISSION_YEARS=6`＋`TACM_ANNUAL_DECAY_PCT=0.25`
- `EmissionAmount(height, blockTime)`：`INITIAL_SUBSIDY × 0.75^year`，6 年發行完畢歸零；**數學封閉總發行＝52,003,300**
- 未設 env 維持既有 halving 模式（零相容破壞）；coinbase 金額與 hash 於上限模式同步重算
- /status 新增 `emission_model/max_supply/emission_years/annual_decay_pct`

### 15.3 會員系統（internal/node/auth.go＋auth_rpc.go＋auth.html）

- users 表（SQLite users.db）、PBKDF2-HMAC-SHA256（100k 迭代、16B salt）、HMAC session（cookie `tacm_sess`、httponly 30 天）
- `POST /api/auth/register|login|logout`＋`GET /api/auth/me`；email 格式/唯一、密碼≥6、錯誤帳密拒
- 頁面 `/auth/login`、`/auth/register`（登入/註冊雙 tab）；頂欄「會員登入」pill，登入後 JS 顯示 email＋點擊登出（全站）

### 15.4 驗證

| 驗證層 | 結果 |
|---|---|
| emission 單元測試（6 年總和＝52,003,300、發行期後 0、首塊 INITIAL_SUBSIDY） | ok |
| auth 單元測試（註冊/重複拒/壞 email 拒/短密碼拒/登入/錯密碼拒/session/偽造拒/cookie） | ok |
| 上限模式實機（env 52003300） | status 顯示 annual_decay/52003300，首塊 coinbase=0.501511778224107 ✓ |
| flat 實機（無 env） | halving、coinbase=10.0（既有行為不變）✓ |
| run_auth_smoke.sh | PASS=11 / FAIL=0 |
| 16 包 go test＋go vet＋m17 8/8＋c2c 26/26 回歸 | 全綠 |

---

## 16. M30 UI 全面幣安化＋App 化（頂部 Python 模式＋底部美化）

### 16.1 變更（僅共用骨架 base.html＋style.css；未改任何子頁業務邏輯）

| 面向 | 內容 |
|---|---|
| 禁縮放 | viewport `maximum-scale=1, user-scalable=no`；`touch-action: manipulation`、按鈕 min-height 44px、輸入 16px（iOS 防縮放） |
| 背景幣安化 | body 疊加金黃/綠微光暈徑向漸層於 `#0b0e11`，全站一致 |
| 頂部（Python 版模式） | brand＋金黃 badge（Python topbar 同款）＋導航；右側白皮書/EN＋社群膠囊（Telegram/X/Discord/官網）＋會員 pill＋網速徽章；行動端僅保留 brand＋工具區 |
| 底部 App 導航 | emoji 改 **SVG symbol icon**（首頁/錢包/挖礦/交易所/社群 5 項）＋active 金黃上緣指示條＋安全區 `env(safe-area-inset-bottom)`＋內容留白防重疊 |
| 通用按鈕 | `.btn/.primary(金黃)/.buy(綠)/.sell(紅)/.ghost/.small`＋active scale 回饋 |
| 膠囊美化 | `.pill` 圓角膠囊、金黃 hover；member-pill 金黃邊框 |

### 16.2 驗證

| 驗證層 | 結果 |
|---|---|
| 9 頁 HTTP（/ /wallet /exchange /mining /community /dashboard /defi /c2c /auth/login） | 全 200 |
| viewport 禁縮放 / SVG 底部導航 / brand-badge / 幣安 CSS | 全部命中 |
| 桌面實機截圖（/wallet） | 幣安風正常、無遮擋、無報錯 |
| 回歸 | 16 包測試＋go vet＋exchange 16/16＋m17 8/8＋auth 11/11＋community 24/24＋mining 13/13，FAIL 全 0 |

---

## 17. M31 線上異常修復（錢包/礦機/社群/頂部/鑄造權限）

### 17.1 根因修復

| 用戶反映 | 根因 | 修復 |
|---|---|---|
| 錢包 TACm 天文數字（1.4e19） | Render 仍為舊版：WalletView 顯示 raw 未縮放 | 新版 `wallet_rpc.go` 已用 `FormatAmountBig`（M29 起）；交付新版重部署 |
| 礦機無反應/「載入中…」卡死 | ① 頁面 RPC base 寫死 `8332`；② refresh `Promise.all` 單一 API 失敗全卡 | ① RPC 預設改**同源**（同埠合併＝web 埠，`?rpc=` 可覆蓋）——community/exchange/mining/wallet 4 頁全修；② refresh 各別 catch 降級 |
| 社群載入失敗/市集「載入中…」 | 同上 ①（community JS 連 8332） | 同源修復後 feed/market/ads 同埠全通 |
| 社群背景 | 深色 | **FB 淺色風**：body `#f0f2f5`、白卡、藍 #1877f2、底欄白化 |
| 頂部橫向滾動 | M30 `overflow-x:auto` | **膠囊式開關選單**：手機端「☰ 工具」膠囊展開下拉（白皮書/EN/社群/會員），不再滾動 |
| 礦機膠囊訊號 | 缺 | 頂部 miner-pill：綠點「挖礦中」/灰點「待機」，10 秒輪詢 /api/miners（Python 版同款訊號） |
| TiUSD 私自鑄造 | 公開 `/api/tiusd/mint|burn` 任何人可發行 | **路由移除（404）**；TiUSD 改由**鏈上機制（供給層 MintTiUSD）**鑄造——defi 池 seed 走鏈上鑄造（實測供給 900,000 自動入池）；錢包頁發行/銷毀表單移除＋說明 |

### 17.2 同步修正

- `wallet_e2e_test.go TestTiUSDRPC`：mint 路由 404 斷言＋鏈上機制 MintTiUSD delta 供給
- `run_m17_smoke.sh`/`run_pool_smoke.sh`：TiUSD 資金準備改交易所直接入金（後台結算等效）

### 17.3 驗證

| 驗證層 | 結果 |
|---|---|
| `/api/tiusd/mint` | 404（禁止私自鑄造）✓ |
| `/api/tiusd/summary` | supply=900,000（鏈上機制自動鑄造）✓ |
| 挖礦完整流程實測 | register→tick→出塊 4 塊→earnings 30.17 TACm（按算力 4vCPU/1vCPU 瓜分正確）→stop ✓ |
| 社群三 API 同埠 | feed/market/ads 200 ✓；頁面實機載入（淺色風、零報錯）✓ |
| 16 包 go test＋go vet | 全綠 |
| 8 層冒煙（auth 11/community 24/mining 13/exchange 16/m17 8/c2c 26/defi 33/pool 14） | FAIL 全 0 |

---

## 18. M32 用戶回饋修復（登入/訊號/算力/錢包分流/節點教學）

### 18.1 修復一覽

| 用戶反映 | 根因 | 修復＋實測 |
|---|---|---|
| 「工具」無法下拉 | M30 CSS `.comm{overflow-x:auto}` 裁切 absolute 下拉 | 移除 overflow；☰ 下拉恢復 |
| 頂部應顯示登入（Python 同款） | 登入入口藏在工具內 | 頂部常顯「登入」pill → 登入後**綠燈＋帳號**＋點擊下拉（我的錢包/礦機/登出） |
| 礦機訊號永遠「待機」 | ① 節點自身默認礦機**註冊後無心跳**（180s 窗口過期）；② DB status 永 in-line 與窗口判定矛盾 | ① 節點**每輪出塊自動心跳**（consensusLoop）；② 徽章與膠囊**統一窗口判定**（`online`＝窗口內新鮮心跳）——實測 online_count=1、綠燈 |
| 礦機個人/全鏈產出區分 | 挖礦頁只有個人收益 | 挖礦頁加**全鏈總覽**（全鏈總產出 TACm／TiUSD 總流通／獎勵池／我的累計收益）；錢包只顯示個人資產 |
| 預設 1vCPU+2vGPU 不得修改 | mining.html 有輸入框＋register 收整數 | **算力固定**：後端 RegisterMiner 忽略傳入（實測傳 99/99 → vcpu=1/vgpu=2/hr=5M）；前端輸入框移除 |
| 算力靠推薦註冊（每人 +0.01CPU/+0.02GPU） | 無推薦機制 | 新增 **referrals 表＋會員綁定錢包地址**：註冊填邀請人地址 → 邀請人算力+0.01/+0.02（實測推薦 2 人 → 1.02/2.04/hr=5.1M） |
| 錢包資產天文數字 | Web 層 `datasource.go WalletView` 用 raw `String()`（與 RPC 層兩套） | 改 `FormatAmountBig/FormatAmountI64`（實測錢包頁顯示 149.6 TACm 非 raw）；獎勵池同步修復 |
| 錢包僅個人＋全鏈總量放礦機 | 錢包有 TiUSD 供給區 | 錢包移除供給區（改「資產說明」）；供給移至挖礦頁全鏈總覽 |
| 節點架設教學 | 無 | 新增 **NODE_SETUP.md**（電腦全節點／手機輕量礦工 PWA／Termux 全節點／驗證表） |

### 18.2 同步修正

- `wallet/miner.go`：vcpu/vgpu 改 REAL、Miner.Online 窗口判定、AddReferral/ReferralCount、RegisterMiner 固定算力
- `node/auth.go`＋`auth_rpc.go`：users 加 wallet_addr/referrer、Register 回傳 walletAddr＋referral_code
- `node/miner_rpc.go`：miners 加 refs/online、新 `/api/chain/stats`（總產出只計 coinbase :miner/:proposer/:pool，排除 seed/defi）
- `base.html`：memberWrap 登入/帳號下拉；`wallet.html`：個人資產＋說明；`mining.html`：全鏈總覽＋固定算力；`auth.html`：邀請碼輸入
- 冒煙：e2e TiUSD 資金改 exchange/deposit；auth_test Register 簽名

### 18.3 驗證

| 驗證層 | 結果 |
|---|---|
| 16 包 go test＋go vet | 全綠 |
| 9 層冒煙（auth 11/community 24/mining 13/exchange 16/m17 8/c2c 26/defi 33/pool 14/e2e 23） | FAIL 全 0 |
| 推薦算力實測 | 註冊傳 99/99 → 固定 1vCPU/2vGPU/5M；推薦 2 人 → 1.02/2.04/5.1M、refs=2 |
| 全鏈總覽實測 | total_mined=出塊累計（2 塊=20）、TiUSD 流通 900,000、獎勵池 2.4 |
| 挖礦頁實機 | 無輸入框、全鏈總覽、登入 pill、綠燈訊號、TiUSD 顯示 900 TiUSD |
| Render API 現況 | `/api/wallet/info` 個人資產正確縮放（7576.8 TACm） |

---

## 19. M33 帳本縮放＋礦機伺服器排程＋即時輪詢＋效率圈

### 19.1 用戶 7 點回饋與修復

| 回饋 | 根因 | 修復＋實測 |
|---|---|---|
| 帳本流固定面板破圖＋變動顯示 raw | 表格無橫向滾動容器；`datasource.go` Delta 直接輸出 raw | ① 表格包 `.tbl-scroll{overflow-x:auto}`（手機左右滾動）；② `formatDelta` 依資產精度縮放並保留符號——實測變動欄顯示 `1.2 / 8.8`（每塊 10×12%/88%），備註截斷 `trunc64` |
| 礦機啟動沒動作／訊號不隨開關／離開頁面失效 | 心跳完全依賴前端 JS（每 10s fetch）——離開頁面即停 → 3 分鐘後離線 | **伺服器端排程**：miners 表加 `active`；新 `POST /api/miner/start\|stop`（start＝註冊＋啟用排程＋即時心跳；stop＝active=0＋心跳歸零）；節點 `minerHeartbeatLoop` goroutine 每 10s 對 active 礦工持續心跳——**實測**：開機 online=True（綠）、關機即時 online=False（灰）、離開頁面 13s 仍線上 |
| 頂部「工具」沒反應／登入後沒顯示 | ☰ toggle 正常（CSS 已查）；「登入後無帳號」＝Render 仍是舊版（新版已含 `/api/auth/me`＋member-wrap） | 本地全流程實測：register→login→`/api/auth/me` 回傳 logged_in＋email → 頂部綠燈＋帳號＋下拉正常 |
| 缺 Python 版即時效率圈／顯示條 | Go 版未搬運 | 挖礦頁新增**效率圓環**（SVG：個人算力佔全鏈比例）＋**CPU/GPU 算力條**（數值＋比例 bar）——實機顯示 100.0% |
| 錢包應僅個人資產（獎勵池屬 DEX） | 錢包頁第 4 張卡顯示獎勵池 | **移除獎勵池卡**（只留 TACm/TiUSD/USDT 個人資產）＋資產說明標註「獎勵池屬交易所資產，於挖礦頁全鏈總覽查看」 |
| 全鏈與個人資產不即時更新（250/900/220 卡死） | `refresh()` 只執行一次，**無輪詢** | mining.html 載入後 `setInterval(refresh, 10000)`——實機：鏈頂#21、總產出 210、收益 184.8（=21×10×88%）持續滾動 |
| 全鏈總覽僅獎勵池會變 | 同上（refresh 無週期） | 同上一併修復（全鏈總產出/TiUSD/我的收益/鏈頂/難度/排行全隨區塊鏈機制即時更新） |
| 疑「可用餘額」重複頁面 | 非重複路由（`uniq -d` 空）；「可用餘額」屬區塊瀏覽地址詳情頁（address.html），與錢包頁不同頁 | 已確認無重複頁面/路由；錢包頁與地址詳情頁功能分離 |

### 19.2 同步修正

- `wallet/miner.go`：miners 表加 `active`；`SetActiveMiner/ActiveMiners/StopMiner`（stop 含 active=0＋心跳歸零）；`Miner.Active` json
- `node/miner_rpc.go`＋`rpc.go`：`POST /api/miner/start`（register＋active＋tick）；`handleMiners` 附 `active`
- `node/node.go`：`minerHeartbeatLoop`（10s tick active 礦工）
- `web/datasource.go`＋`server.go`：`formatDelta`（帳本縮放）＋`trunc64` filter
- `web/templates/wallet.html`：去獎勵池卡＋10s 資產/區塊即時刷新＋`tbl-scroll`
- `web/templates/mining.html`：效率圓環＋算力條＋`setInterval(refresh,10000)`＋start/stop 走伺服器排程＋按鈕狀態恢復（F5 正確）
- `run_mining_smoke.sh`：新增 4 項排程測試（active/online/即時灰/持續心跳）

### 19.3 驗證

| 驗證層 | 結果 |
|---|---|
| 16 包 go test＋go vet | 全綠 |
| 9 層冒煙 | mining 17/17（新增 4 項排程測）、auth 11、community 24、exchange 16、m17 8、c2c 26、defi 33、pool 14、e2e 23 —— FAIL 全 0 |
| 伺服器排程實測 | 開機綠→關機灰→無前端 13s 仍綠 |
| 帳本縮放實測 | `1.2 / 8.8` 十進制（非 raw）＋tbl-scroll |
| 錢包頁實機 | 3 資產卡（無獎勵池）、TACm 325.6 縮放正確 |
| 挖礦頁實機 | 效率圈 100.0%、停止鈕正確、全鏈總覽隨出塊滾動（#21→210/184.8） |
| 登入流程實測 | register→login→/api/auth/me 回傳 logged_in＋email |

## §20 M34：線上 7 點異常根因修復＋全設備適配（2026-10-08）

**用戶線上截圖反饋（Render 舊版＋sw v1 快取）**：①頂部「待機」不變＋登入自動挖礦＋未啟動即挖；②礦機關閉仍挖；③挖礦頁全鏈總覽 250/900/220 固定不動僅獎勵池動；④首頁無全鏈資訊/速率圈；⑤錢包帳本破版；⑥全局全設備自適應、禁縮放禁反白；⑦頂部三膠囊異常。

### 根因
1. **全鏈總覽固定**＝wallet/Store.Ledger() 對 limit>500 硬重置 50 條＋/api/chain/stats 與 /api/miner/earnings 只掃 25 塊（250/220 永不動；獎勵池直接查餘額所以會動）。
2. **線上混合舊頁**＝sw.js v1 cache-first 把舊 HTML 快取住。
3. **pill「待機」**＝舊版無輪詢＋被快取；sync 只在 DOM 就緒一次。
4. **登入/綁定**＝/api/auth/me 不回 wallet_addr，mining 綁定失效。

### 修改
| 檔案 | 變更 |
|---|---|
| internal/wallet/store.go | 新增 chain_stats 表（id=1, total_mined_tacm, updated_ts）＋AddMinedTacm/ChainMinedTacm（O(1)）；Ledger cap 改 5000（修 limit>500 硬重置） |
| internal/wallet/sync.go | ApplyBlock coinbase（:miner/:proposer/:pool）分發後累加 minedTotal（fallback 與 splits 兩分支） |
| internal/wallet/wallet.go | Reward() hook 已還原（coinbase 走 ApplyBlock，不走 Reward） |
| internal/node/miner_rpc.go | handleChainStats 改讀 counter（total_mined_tacm） |
| internal/web/datasource.go | ChainStatsView＋ChainStats()（counter＋獎勵池＋TiUSD 供給＋在線礦工算力）；DataSource interface＋fakeDS 補齊 |
| internal/web/handlers.go | handleIndex 傳 chain 資料 |
| internal/web/templates/index.html | 全鏈儀表 panel（速率圈 SVG＋全鏈總產出/獎勵池/TiUSD/出塊速率）＋10s 刷新 |
| internal/web/templates/mining.html | bindMiner 會員綁定（me→wallet_addr；訪客→節點＋「（訪客）」標註）；myAddrOnline 依 render 設定；全鏈總覽 10s 滾動 |
| internal/web/templates/base.html | pillSync 會員導向（登入＝會員錢包；訪客＝節點地址）＋5s 輪詢＋tac-pill-sync 事件 |
| internal/web/templates/wallet.html | 帳本 tshort（MM-DD HH:mm）＋trunc40（備註 40 字截斷）＋.tbl-scroll 橫向滾動 |
| internal/web/server.go | 新增 tshort/trunc40 template filter |
| internal/web/static/css/style.css | 全域 user-select:none＋tap-highlight 透明＋touch-action:manipulation；.tbl-scroll min-width 560px＋ellipsis |
| internal/web/static/sw.js | v2：API network-only、頁面 network-first、靜態 stale-while-revalidate |
| internal/node/auth.go | AuthUser 加 WalletAddr（json:"wallet_addr"）；VerifySession 查 wallet_addr |

### 實機驗證（本地 8892，block-time=1）
- 全鏈總覽即時：t0 total_mined=20/pool 2.4 → t+3s 50/6；截圖 160/19.2/140.8、360/43.2/316.8、610/73.2/536.8、1520/182.4/1337.6 均＝區塊數×10/1.2/8.8 精確。
- 我的累計收益：41 塊×8.8＝360.8 ✓。
- 會員綁定：註冊→/api/auth/me 回 wallet_addr（tx013Foz…）✓；miner/start 後 miners 該地址 active=True online=True（online_count 1→2）；stop 即時 offline（2→1）✓。
- pill：訪客（節點恒在線）頁面載入後不再誤顯示「待機」；JS 5s 校正＋mining render 雙保險 ✓。
- 錢包帳本：.tbl-scroll 容器限寬橫向滾動，不再溢破版面 ✓。
- 首頁儀表：全鏈 80/9.6/900/8 塊＋速率圈 ✓。

### 回歸
- go test ./...：16 包 ok、0 FAIL（node 66s e2e 含）。
- 9 支冒煙：auth 11 / community 24 / mining 17 / exchange 16 / m17 8 / c2c 26 / defi 33 / pool 14 / e2e 23，合計 172 項全 PASS、0 失敗。
- 打包 tacm-go-m34.zip（排除 .smoke_*/.fix_pools.py/tacweb/tacnode/.regress.sh/.ck.txt）。

### 交付與上線注意
- **Render 必須 Manual Deploy 新 commit**（GitHub 自動部署未驗證）。線上 sw v1 cache-first 是線上所有「異常」元兇——部署後**清除瀏覽器快取/改用無痕**再測。
- Render 免費層無持久碟：重啟後鏈/錢包重置為創世（測試用途）。

## §21 M35 收尾：礦機按鈕同步＋登入帳號＋工具下拉＋白皮書中英＋TiUSD 錨定（2026-10-09）

### 修改檔
| 檔案 | 內容 |
|---|---|
| internal/wallet/supply.go（新） | SetTACMSupplyCap／tiUSDSupplies（floor＝總供應×0.330、cap＝總供應×1e6）／EnsureTiUSDFloor（低於下限自動補鑄至下限） |
| internal/wallet/supply_test.go（新） | TestTiUSDSupplyFloorAndCaps（floor/cap 邊界全綠） |
| internal/wallet/wallet.go | mu sync.Mutex＋tacmMaxSupply；MintTiUSD 超上限拒絕、BurnTiUSD 低於下限拒絕 |
| internal/node/node.go | LoadEmission 傳參修正（int64(bt)→int64(cfg.BlockTime)，修 ns 誤傳致發行爆量）；SetTACMSupplyCap＋EnsureTiUSDFloor 注入（Model==annual_decay 且 MaxSupply>0） |
| internal/chaindb/chaindb.go | FormatFloat 由 'g' 改 'f'（消除科學記號 1.18e+07 致 coinbase 金額解析失敗） |
| internal/web/static/js/i18n.js（新） | 全套中英字典＋TAC_I18N{cur,apply,toggle}＋localStorage 持久化＋?lang=en/zh URL 切換 |
| internal/web/templates/base.html | data-i18n 全導航/膠囊/工具列/底部＋langToggle 切換鈕＋帳號縮排（👤 email 前 9 字…）＋移除 miner-label/member-label data-i18n（防 apply 覆寫 paint）＋member 監聽 tac-lang-changed 即時切語 |
| internal/web/templates/mining.html | 按鈕以 on（online）判定「停止挖礦/開機挖礦」＋徽章 i18n＋tiusd 顯示 /1e6 |
| internal/web/templates/wallet.html | data-i18n 標籤 |
| internal/web/templates/index.html | data-i18n＋tiusd 顯示 /1e6 |
| internal/web/static/whitepaper_zh.html / _en.html | §6.3 出塊即時瓜分（88%/12%/180s 心跳）、§6.4 礦機算力（1vCPU+2vGPU 固定、推薦 +0.01/+0.02、邀請碼＝tx0 地址）、§6.5 代幣分配、§7.1 幣安風交易所（費率 TACm 200bp/TiUSD 50bp/USDT 125bp）、§8 供給層鑄造＋0.330 下限/1:1 上限；兩檔 langbar 中英互跳 |
| run_wallet_smoke.sh | 步驟 2 改為錨定 summary＋公開 mint 路由 404（禁止私自鑄造）驗證 |

### 鏈核心 Bug 修復（M35 收尾挖出）
1. FormatFloat 'g' 科學記號（1.18e+07）→ coinbase 金額解析失敗 → 錢包同步卡死（全鏈總覽 0）。改 'f' 後 0 錯誤。
2. LoadEmission 誤傳 int64(bt)（Duration ns）→ 發行曲線爆量（16 塊挖 36M）。改 int64(cfg.BlockTime) 後每塊 ~0.33 TACm（6 年發完 52,003,300）。

### 實機驗證（TACM_MAX_SUPPLY=52003300，block-time=1）
- coinbase 入帳：total_mined_tacm 即時累計、reward_pool＝12%、我的累計收益＝88% ✓
- mining 頁：在線→「停止挖礦」；離線→「開機挖礦」；頂欄訊號與頁面一致（挖礦中）✓
- 全鏈總覽四數值隨出塊即時滾動 ✓
- ?lang=en：Stop Mining／Total TACm Mined／Reward Pool／My Earnings／Whitepaper／「中文」切換鈕全英文 ✓
- 白皮書中英互跳（English/中文 切換條＋章節目錄）✓
- TiUSD：tiusd_supply=18,061,089,000,000 micro＝floor(17,161,089,000,000)＋DeFi seed(900,000,000,000)，錨定上限 1:1＝52,003,300×1e6 ✓

### 回歸
- go test ./...：16 包全 ok、0 FAIL。
- 冒煙 10 支全綠：auth / mining / wallet 8 / exchange / community / m17 / c2c / defi / pool / e2e 23（合計 172 項 PASS、0 失敗）。
- 打包 tacm-go-m35.zip＋render-tac-chain.zip（排除 .smoke_*/.smoke2/tacweb/tacnode/.regress.sh/.ck.txt/run_*.sh 調試目錄）。

### 上線注意
- Render 環境變數必須加 **TACM_MAX_SUPPLY=52003300**（否則錨定不啟用、走 halving）。
- Render Start 命令：`./tacweb -web-port $PORT -rpc-port $PORT -data-dir ./data -block-time 1 -difficulty 1`。
- 線上舊 sw 快取：部署後無痕／清除快取再測。
