#!/bin/bash
# M29 會員系統冒煙（真進程、真 DB、cookie 會話）
set -u
cd "$(dirname "$0")"
W=8790; B=8791; D=.smoke_auth; LOG=.smoke_auth.log
PY=/opt/python3.12/bin/python3
pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$D"
TAC_AUTH_SECRET=smoke-secret ./tacweb -data-dir "$D" -web-port $W -rpc-port $B -block-time 1 -difficulty 1 >"$LOG" 2>&1 &
sleep 2.5
J() { curl -s -c "$D/cj" -b "$D/cj" -H 'Content-Type: application/json' "$@"; }
ok=0; bad=0
ok(){ ok=$((ok+1)); echo "[auth] PASS: $1"; }
bad(){ bad=$((bad+1)); echo "[auth] FAIL: $1"; }
# 1 註冊
R=$(J -X POST http://127.0.0.1:$B/api/auth/register -d '{"email":"user1@tac.chain","password":"abc123"}')
echo "$R" | grep -q '"ok":true' && ok "註冊成功（user1@tac.chain）" || bad "註冊: $R"
# 2 重複 email 拒
R=$(J -X POST http://127.0.0.1:$B/api/auth/register -d '{"email":"user1@tac.chain","password":"abc123"}')
echo "$R" | grep -q '已被註冊' && ok "重複 email 被拒" || bad "重複 email: $R"
# 3 壞 email 拒
R=$(J -X POST http://127.0.0.1:$B/api/auth/register -d '{"email":"bad","password":"abc123"}')
echo "$R" | grep -q '格式不正確' && ok "壞 email 被拒" || bad "壞 email: $R"
# 4 短密碼拒
R=$(J -X POST http://127.0.0.1:$B/api/auth/register -d '{"email":"u2@tac.chain","password":"123"}')
echo "$R" | grep -q '至少 6 碼' && ok "短密碼被拒" || bad "短密碼: $R"
# 5 錯誤密碼登入拒
R=$(J -X POST http://127.0.0.1:$B/api/auth/login -d '{"email":"user1@tac.chain","password":"wrong!"}')
echo "$R" | grep -q '帳號或密碼錯誤' && ok "錯誤密碼被拒" || bad "錯密碼: $R"
# 6 登入成功（cookie 存於 cj）
R=$(J -X POST http://127.0.0.1:$B/api/auth/login -d '{"email":"user1@tac.chain","password":"abc123"}')
echo "$R" | grep -q '"ok":true' && ok "登入成功" || bad "登入: $R"
# 7 me（已登入）
R=$(J http://127.0.0.1:$B/api/auth/me)
echo "$R" | grep -q '"logged_in":true' && echo "$R" | grep -q 'user1@tac.chain' && ok "me 回傳登入會員" || bad "me: $R"
# 8 登出
R=$(J -X POST http://127.0.0.1:$B/api/auth/logout)
echo "$R" | grep -q '"ok":true' && ok "登出成功" || bad "登出: $R"
# 9 me（登出後）
R=$(J http://127.0.0.1:$B/api/auth/me)
echo "$R" | grep -q '"logged_in":false' && ok "登出後 me=false" || bad "me 登出後: $R"
# 10 auth 頁面
curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$W/auth/login | grep -q 200 && ok "/auth/login 頁面 200" || bad "auth 頁面"
# 11 頂欄會員入口
curl -s http://127.0.0.1:$W/wallet | grep -q 'member-pill' && ok "頂欄會員入口存在" || bad "頂欄會員入口"
pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null
echo "================ 結果：PASS=$ok FAIL=$bad ================"
[ "$bad" = "0" ]
