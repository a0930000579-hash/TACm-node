# TAC Ai Smart Chain — Node Console (M86)

節點自 M86 起**獨立成自有品牌**，與主網用戶功能（landing / 錢包 / 交易所 / 挖礦等）完全分離。

## 節點獨立資料夾（檔案自成一個資料夾）

| 路徑 | 內容 |
|---|---|
| `cmd/nodeweb/` | 節點獨立可執行檔（binary：`nodeweb`）——只啟動「節點 + 節點品牌儀表板」 |
| `internal/nodeui/` | 節點品牌 UI 包：`nodeui.go`（Server/handler）、`templates/index.html`（品牌儀表）、`static/node.css`（亮色綠/黃品牌）、`static/nodeui.js`（i18n 中英 + 10s 輪詢）、`static/tacnode.svg` |
| `internal/p2p/` | M86-B 批量同步（catch-up sync）：`GET /p2p/blocks` 批次端點、`syncOnce` 批次追塊 + 失敗降級單塊、gossip 缺口立即觸發同步 |
| `NODE_UI.md` | 本文件 |

主網側（`internal/web`）新增 `GET /node` 說明頁，導引節點運維者開啟自己的品牌控制台。

## 節點品牌儀表板內容

- **鏈狀態卡**：鏈頂高度 / 最終化 / 動態難度 / 同步進度（落後最佳節點塊數）
- **節點身分**：節點地址、Node ID、身分金鑰、運行時間、Base Fee、共識機制
- **節點獎勵**：每塊 10 TACM 分潤說明（節點 9% / pool 16% / 礦工 75%）
- **P2P Peers**：在線/離線、高度、最終化（`PeerSnapshot()` 公開 API）
- **驗證人集**：地址、權重、總權重、quorum、fault tolerance
- **最新區塊**：出塊者、交易數、難度
- **加入指引**：啟動指令（一次一條）＋種子節點
- **開啟主網**：一鍵連回開放挖礦主網入口

## 啟動指令（一次一條，於節點伺服器執行）

```bash
sudo systemctl stop tacnode
```
```bash
rm -rf /var/tac/data
```
```bash
curl -L -o /tmp/tacm-go-m86.zip '<壓縮檔 aka>'
```
```bash
cd /root && sudo unzip -o /tmp/tacm-go-m86.zip
```
```bash
cd /root/tacm-go && GOTOOLCHAIN=local go build -o tacweb ./cmd/web
```
```bash
cd /root/tacm-go && GOTOOLCHAIN=local go build -o nodeweb ./cmd/nodeweb
```
```bash
sudo systemctl restart tacnode
```
```bash
curl -s http://2.28.201.174:8080/status
```
```bash
curl -s http://2.28.201.174:8080/api/audit
```
```bash
curl -s http://2.28.201.174:8080/node
```

節點品牌控制台（獨立 binary，follower 模式，UI 埠 8081）：

```bash
sudo ./nodeweb -web-port 8081 -p2p-url http://<你的公網IP>:8081 -p2p-seed http://2.28.201.174:8080 -p2p-follower
```

開啟 `http://<你的公網IP>:8081` 即見「TAC Ai Smart Chain — Node」品牌儀表板。

## M86-B 批量同步（同步 flaky 根治）

- seed 端新增 `GET /p2p/blocks?from=&to=`（上限 500 塊/批，鏈頂截斷）。
- `syncOnce` 改批次拉取；批次內某塊失敗時降級單塊重試（不再整輪中止等下一 tick）。
- gossip 收到「區塊缺口」錯誤時**立即觸發一次 syncOnce**（不等週期 tick）。
- 驗證：`TestP2PLedgerConsistency` 連續 5 跑 5/5 通過（舊版 80% 通過率）。
