tacweb v4（M147c + p2p-url 自動偵測 + 節點架設者面板 M148）

新增：
1. -p2p-url 自動偵測（RENDER_EXTERNAL_URL 等平台環境變數），架設者免改網址
2. 節點架設者面板 /node-panel：
   - 節點狀態與節點獎勵餘額（coinbase:node 9% 鏈上）
   - 設置管理密碼（首次）
   - 綁定收款地址（tac web3 錢包 tx01...）
   - 提領：節點私鑰簽名把節點獎勵轉入綁定錢包（上鏈可審計）
3. 節點名稱修正：render1/follower → node1/驗證節點、錨點

部署：
1. curl -L -o /tmp/tacweb-v4.zip "https://aka.doubaocdn.com/s/TBD"
2. cd /tmp && unzip -o tacweb-v4.zip
3. sudo systemctl stop tacnode
4. sudo cp /tmp/tacweb /root/tacm-go/tacweb
5. sudo chmod +x /root/tacm-go/tacweb
6. sudo systemctl start tacnode
7. 驗證：curl -s "http://127.0.0.1:8080/api/audit?blocks=100" | grep -o "coinbase_pass.:[0-9]*\|consistent.:[a-z]*"
8. 節點面板：瀏覽器開 http://2.28.201.174:8080/node-panel
