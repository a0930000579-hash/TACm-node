# TAC Ai Smart Chain — Security Posture Report
# TAC Ai 智能鏈 — 安全自檢報告（v1.0 / M41）

> 本報告盤點 TAC 鏈已內建的安全機制、已知風險邊界與建議的外部審計項目。
> 目的：為「上市級」商業化提供可追溯的安全證據清單。

---

## 1. 已內建安全機制（Built-in Controls）

| 層面 | 機制 | 實作位置 |
|---|---|---|
| 交易簽名 | secp256k1 ECDSA 簽名驗證（入池前強制） | internal/crypto, internal/node/submit.go |
| 防重放 | 每帳戶 Nonce 順序校驗，拒絕亂序/重複 | SubmitTransaction（GetNonce 檢查） |
| 餘額安全 | 交易前餘額校驗；不足即拒絕；SQLite 事務原子寫入 | chaindb / submit.go |
| 合約安全 | 合約交易入池前只讀模擬（失敗即拒）；位元組碼/地址/金額嚴格校驗 | internal/vm, node 模擬 |
| 出塊安全 | PoW 前導零驗證；區塊 Hash/Merkle 重算驗證 | consensus/difficulty, chaindb |
| 最終性 | BFT 投票 >2/3 權重門檻；多數認證 View Change；Slashing 5% 罰沒 | consensus/pos, consensus/bft |
| 治理安全 | 提案參數上下限校驗（如 difficulty 1–100、tiusd_floor 1000–5000） | internal/governance |
| 跨鏈安全 | SPV Merkle 證明 + 區塊頭鏈驗證；守衛多簽 >2/3 才執行 | internal/spv, internal/bridge |
| 供應安全 | 總供應上限 52,003,300 鏈上強制（EmissionError 校驗） | internal/chaindb/emission.go |
| 穩定幣安全 | TiUSD 流通下限 0.330×TACM 供應，低於下限自動補鑄（防崩盤） | chaindb / node |
| 存儲安全 | SQLite 事務原子性；重啟恢復；資料目錄網段分離（M41） | chaindb, config |
| 身份安全 | 節點/礦工密鑰獨立；訪客獨立地址；會員礦機綁定 | node identity, wallet |

## 2. 已知風險邊界（Residual Risks）

| 風險 | 說明 | 建議緩解 |
|---|---|---|
| 單節點部署 | Render 免費層目前單實例運行，無多節點攻擊面 | 雲端多節點（付費層/自架），tacnet.sh 已驗證 3 節點 |
| 無外部審計 | 尚未經第三方安全公司代碼審查 | 上市前委託獨立審計（程式碼審查+模糊測試+滲透） |
| RPC 開放面 | 節點 RPC 監聽 0.0.0.0，無內建 API 金鑰/速率限制 | 反向代理加 TLS＋IP 白名單＋速率限制 |
| 治理少數 | 單節點上治理權重集中於本機帳戶 | 多節點分散後權重自然分散；驗證人多元 |
| 記憶池放大 | 無 tx 大小上限之外的反垃圾機制 | 增加每帳戶記憶池配額（路線圖） |

## 3. 建議外部審計清單（Pre-Launch Checklist）

1. 程式碼安全審查（共識/VM/橋/錢包/交易所核心）。
2. 模糊測試（交易/區塊/合約位元組碼/治理輸入）。
3. 滲透測試（RPC/Web/登入/帳號管理）。
4. 經濟模型壓力測試（發行曲線/TiUSD 錨定/獎勵池）。
5. 多節點網路攻擊模擬（雙花/延遲/分叉）。

## 4. 合規與商業化準備

- 代幣經濟：供應上限、年衰減曲線、獎勵池分潤（12%）、TiUSD 錨定下限 0.330×——均已鏈上固化，可審計。
- 監控：/api/health（存活探針）＋ /api/metrics（運行指標）＋ 治理 API——供營運/監控告警。
- 文獻：白皮書（Web＋PDF 中英）、技術黃皮書——投資人/合作方技術盡職調查材料。
