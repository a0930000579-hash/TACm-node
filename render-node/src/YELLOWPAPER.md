# TAC Ai Smart Chain — Technical Yellowpaper
# TAC Ai 智能鏈 — 技術黃皮書（v1.0 / M40）

> TAC 是一條自主研發的 Layer-1 區塊鏈（不依附任何外部鏈），整合 PoW 出塊、BFT+PoS 最終性、鏈上治理、智能合約（TAC VM）、Rollup L2、跨鏈橋與去中心化交易所生態。本文為其技術規格總覽，所有數值對應真實實作（Go 語言）。

---

## 1. System Overview / 系統概述

| 層級 | 元件 | 說明 |
|---|---|---|
| 共識 | PoW + Dynamic Difficulty | 工作量證明出塊，每 10 區塊重定標難度 |
| 最終性 | BFT + PoS + View Change | 驗證人投票即時最終化；Slashing 罰沒；多數認證切輪 |
| 網路 | P2P | 多節點廣播區塊/交易/共識投票/跨鏈提案 |
| 執行 | TAC VM | TAC VM 智慧合約解釋器（WorldState/Storage/Gas） |
| 擴容 | Optimistic Rollup L2 | L2 狀態樹 + 詐欺證明 + L1 錨定 |
| 互通 | Cross-Chain Bridge | 鎖定/鑄造狀態機 + SPV 輕客戶端 + 守衛多簽 |
| 治理 | On-Chain Governance | 提案/權重投票/到期自動執行參數 |
| 生態 | 錢包/交易所/DeFi/C2C/社群/礦機 | 內建商業閉環 |

## 2. Cryptography / 密碼學

- 簽名：`secp256k1` ECDSA（RFC6979 確定性），DER 編碼。
- 哈希：`SHA-256`；交易哈希 = `DoubleSHA256(Canonical({p:核心欄位, sig:簽名}))`；區塊 Merkle 根 = 交易哈希 SHA-256 Merkle。
- 地址：Base58Check，由壓縮公鑰（33 bytes，02/03||X）派生，`tx0` 前綴。
- 效能基準：SHA256 ≈ 725 萬次/s；簽名 ≈ 9,500/s；驗證 ≈ 3,500/s；1000 tx Merkle ≈ 1,800/s。

## 3. Block Structure / 區塊結構

```
Block { Height, Hash, PrevHash, MerkleRoot, Proposer, ProposerAddress,
        Ts, TxCount, Difficulty, Nonce, Size }
Transaction { TxHash, BlockHeight, BlockHash, TxIndex, FromAddr, ToAddr,
              Amount, Fee, Nonce, Ts, Signature, Pubkey, Memo, Status }
```
- PoW：`SHA256(header + nonce)` 需滿足前導零 ≥ 有效難度（上限 8；失敗逐級降級重試最多 3 次）。
- 創世區塊自動生成；coinbase 獎勵置於每區塊首位。

## 4. Consensus / 共識

- **PoW 出塊**：動態間隔出塊；每 `RetargetInterval=10` 區塊按窗口時間重算基礎難度（維持目標出塊時間）。
- **BFT 最終性**：驗證人（創世設定 `nodeID:address:pubkey:power`）對新塊投票（precommit），`>2/3` 權重達成最終化（FinalizedHeight）；`pow_bft_distributed` 模式多節點輪值出塊。
- **View Change**：round 超時切換；多數認證（>2/3 驗證人簽名）才切換 proposer；`SlashRate=5%` 罰沒作惡驗證人。
- **防重放**：每帳戶 Nonce 順序校驗（`GetNonce`），拒絕亂序/重複交易。

## 5. State & Accounts / 狀態與帳戶

- SQLite（`chain.db`）：`blocks / transactions / accounts / mempool / peers`。
- 多資產帳戶模型：TACm（原生幣）、TiUSD（穩定幣）。
- 帳戶餘額以字串精確存儲；手續費：TACm 2% / TiUSD 0.5%

## 6. Tokenomics / 代幣經濟

- **總供應上限**：`TACM_MAX_SUPPLY = 52,003,300`。
- **發行**：年衰減模型（`TACM_EMISSION_YEARS=6`、`TACM_ANNUAL_DECAY_PCT=25%`）——每區塊獎勵由當前高度與衰減曲線決定，鏈上帳本一致。
- **獎勵池**：coinbase 12%（`PoolShareBps=1200`）＋交易手續費挹注 → 作為去中心化交易所資金池。
- **TiUSD 錨定**：流通下限 = TACM 總供應 × 0.330（`TiUSDFloorBps=3300`）；低於下限自動補鑄；精度 1e6 micro。

## 7. Smart Contracts / 智能合約（TAC VM）

- TAC VM 架構：WorldState（帳戶 code/storage）+ 位元組碼解釋器 + Gas 計費。
- 支援：合約部署、呼叫、storage 讀寫、呼叫棧、錯誤回滾；管理器註冊進節點；合約交易入池前只讀模擬（失敗即拒）。
- 防注入：地址/金額/位元組碼嚴格校驗；`isContractMemo` 路由。

## 8. Layer-2 Rollup / L2

- Optimistic Rollup：L2 狀態樹（帳戶/儲存/區塊）持久化 `l2.db`；批次提交 L1 錨定。
- 詐欺證明：批次執行結果 vs 挑戰者執行結果不一致 → 挑戰流程（fraud proof）。
- L1 端 Service 背景同步；`-l2` flag 啟用。

## 9. Cross-Chain Bridge / 跨鏈橋

- 鎖定/鑄造狀態機：來源鏈鎖定 → 目標鏈鑄造；銷毀 → 解鎖；事件簽名（ECDSA）驗證。
- SPV 輕客戶端：Merkle 證明 + 區塊頭鏈驗證（`-bridge`、`-bridge-guardians`）。
- 守衛多簽：跨鏈提案經 P2P 傳播聚合 >2/3 守衛簽名才執行。

## 10. On-Chain Governance / 鏈上治理

- 提案類型：`param`（參數調整）/ `treasury`（國庫支出）/ `meta`（治理決議）。
- 生命週期：提案（StartHeight）→ 投票窗口 `VotingWindow=20` 區塊 → 到期 `TallyAndExecute(height)` 自動統計。
- 投票權重：`1 + min(TACm 餘額/1e6, 9)`（持幣加成，上限 10）。
- 通過門檻：贊成權重 ≥ 已投票權重 × `PassThreshold=2/3`；參數提案通過即熱更新（block_time / difficulty 立即生效）。
- 可治理參數：`block_time`（0.5–60s）、`difficulty`（1–100）、`tx_fee_bps`（0–5000）、`tiusd_floor_bps`（1000–5000）。

## 11. Network & Deployment / 網路與部署

- P2P：節點間廣播 Tx / Block / 共識投票 / View Change / 跨鏈提案。
- 多節點：`tacnet.sh start|stop|status` 一鍵 3 節點（port 8332/8333/8334）分佈式 BFT；身份目錄 = 啟動 data-dir，避免身份碰撞。
- 單檔部署：`tacweb`（web 合併模式）／`tacnode`（純節點）；SQLite 單檔、零外部依賴（純 Go）。
- 監控：`/api/health`（存活探針）、`/api/metrics`（鏈頂/難度/記憶池/線上礦工/全網算力/供應）。

## 12. Performance Evidence / 效能證據

| 基準 | 數值 |
|---|---|
| 區塊打包（100 tx/塊） | ~128 blocks/s ≈ 12,800 tx/s 寫入 |
| 區塊讀取（200 塊） | ~2,000,000 blocks/s |
| 記憶池吞吐（1000 tx 寫+讀） | ~5,100 tx/s |
| ECDSA 簽名 | ~9,500/s |
| ECDSA 驗證 | ~3,500/s |
| SHA-256 | ~7,250,000/s |

*指令：`go test ./internal/crypto/ -bench .` 與 `go test ./internal/chaindb/ -bench .`*

## 13. Security Posture / 安全態勢

- 已內建：簽名驗證、Nonce 防重放、餘額校驗、合約模擬預檢、PoW 驗證、BFT 投票權重門檻、Slashing、SPV 驗證、多簽守衛、治理參數範圍校驗、SQLite 事務原子性。
- 建議外部：獨立第三方安全審計（程式碼審查 + 模糊測試）、主網/測試網分離後的多節點攻擊面測試。

## 14. Roadmap / 路線圖

- M40（當前）：鏈上治理、壓力測試、白皮書 PDF、技術黃皮書。
- 下一階段：主網/測試網網路隔離、合約擴充指令集、多節點雲端部署、第三方審計。
