tacweb v2（M147c + 節點名稱修正）

變更：節點頁 render1/follower → node1/驗證節點（純顯示，不影響共識）

部署：
1. curl -L -o /tmp/tacweb-v2.zip "https://aka.doubaocdn.com/s/TBD"
2. cd /tmp && unzip -o tacweb-v2.zip
3. sudo systemctl stop tacnode
4. sudo cp /tmp/tacweb-v2-pkg/tacweb /root/tacm-go/tacweb
5. sudo chmod +x /root/tacm-go/tacweb
6. sudo systemctl start tacnode
7. 驗證：curl -s "http://127.0.0.1:8080/api/audit?blocks=100" | grep -o "coinbase_pass.:[0-9]*\|consistent.:[a-z]*"
   目標 coinbase_pass:100 consistent:true
