#!/usr/bin/env bash
# TAC 自主智能鏈 — 全鏈路端到端實機驗證（E2E）。
# 單一進程（tacweb＝節點+RPC+Web）、同一資料目錄，一次串跑完整業務流：
# 出塊 → 鏈上轉帳 → TiUSD → 入金互通 → 撮合 → 閃兌 → 提現 → 獎勵池 → 聚合 → 儀表板。
# 全部以 wei 整數斷言，守恆可審計。PASS/FAIL 逐項計數。
set -u
RPC=8780; WEB=8779
ROOT="$(cd "$(dirname "$0")" && pwd)"
DATA="$ROOT/.smoke_e2e"
PASS=0; FAIL=0
log()  { echo "[e2e] $*"; }
ok()   { PASS=$((PASS+1)); log "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); log "FAIL: $1"; }

pkill -x tacnode 2>/dev/null; pkill -x tacweb 2>/dev/null; sleep 0.5
rm -rf "$DATA"; mkdir -p "$DATA"
(cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web) || { echo "build failed"; exit 2; }
nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $RPC -block-time 1 -difficulty 1 >"$DATA/web.log" 2>&1 &
WPID=$!
trap 'kill $WPID 2>/dev/null; pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null' EXIT
for i in $(seq 1 25); do curl -sf "http://127.0.0.1:$RPC/health" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "http://127.0.0.1:$RPC/health" >/dev/null 2>&1 && ok "節點+Web 啟動" || bad "啟動"

J() { curl -s -X POST "http://127.0.0.1:$RPC$1" -H 'Content-Type: application/json' -d "$2"; }
W() { curl -s "http://127.0.0.1:$RPC/api/wallet/info?address=$1"; }
EX() { curl -s "http://127.0.0.1:$RPC/api/exchange/balances?uid=$1"; }
BIG() { python3 -c "import sys;v=sys.argv[1];print(v)"; }

U1="tx0E2EA"; U2="tx0E2EB"
WEI_TACM='1000000000000000000'

# 1. 出塊
sleep 2
H=$(curl -sf "http://127.0.0.1:$RPC/status" | python3 -c "import sys,json;print(json.load(sys.stdin)['block_height'])")
[ "$H" -ge 1 ] && ok "出塊 height=$H" || bad "出塊 height=$H"

# 2. 種子資金：U1 TACm 10（外部充值）、U2 TACm 10；TiUSD mint U1 500 / U2 500。
J /api/wallet/deposit "{\"to\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"s\"}" >/dev/null
J /api/wallet/deposit "{\"to\":\"$U2\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"s\"}" >/dev/null
# M31：TiUSD 禁止私自鑄造——改交易所資金池直接入金（後台結算等效）。
J /api/exchange/deposit "{\"uid\":\"$U1\",\"asset\":\"TiUSD\",\"amount\":\"500\"}" >/dev/null
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"500\"}" >/dev/null

# 3. 鏈上轉帳 U1→U2 1 TACm（費 2%＝0.02，fee 入錢包 fee 帳戶）。
R=$(J /api/wallet/transfer "{\"from\":\"$U1\",\"to\":\"$U2\",\"asset\":\"TACm\",\"amount\":\"1\",\"memo\":\"t\"}")
echo "$R" | grep -q '"ok":true' && ok "鏈上轉帳 1 TACM" || bad "轉帳: $R"
W1=$(W "$U1"); W2=$(W "$U2")
B1=$(echo "$W1" | python3 -c "import sys,json;print(json.load(sys.stdin)['tacm_raw'])")
B2=$(echo "$W2" | python3 -c "import sys,json;print(json.load(sys.stdin)['tacm_raw'])")
python3 -c "import sys;assert int('$B1')==10*10**18-1020000000000000000 and int('$B2')==10*10**18+1000000000000000000,sys.exit(1)" && ok "轉帳後 U1=8.98 / U2=11（wei 精確）" || bad "轉帳守恆: U1=$B1 U2=$B2"

# 4. 入金互通：U1 TACm 5 → 交易所（鏈上扣 5+0.1）。
R=$(J /api/exchange/deposit_from_wallet "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"5\"}")
echo "$R" | grep -q '"ok":true' && ok "入金互通 TACm5" || bad "入金: $R"
E1=$(EX "$U1")
echo "$E1" | grep -q '"avail":"5"' && ok "交易所入帳 5" || bad "ex 入帳: $E1"

# 5. U2 入金 TiUSD 300 → 撮合：U1 賣 2@100、U2 買 2（taker 0.5% TACm、maker 0.25% TiUSD）。
J /api/exchange/deposit_from_wallet "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"300\"}" >/dev/null
J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/TiUSD\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}" >/dev/null
R=$(J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/TiUSD\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}")
echo "$R" | grep -q '"status":"filled"' && ok "撮合成交 2 TACM @100 TiUSD" || bad "撮合: $R"
R=$(curl -s "http://127.0.0.1:$RPC/api/exchange/fee")
echo "$R" | grep -q '"TACm","avail":"0.01"' && echo "$R" | grep -q '"TiUSD","avail":"0.5"' && ok "fee 帳戶：TACm 0.01 + TiUSD 0.5" || bad "fee: $R"

# 6. 閃兌：U2 掛限價賣 1@100 提供賣單深度；U1 賣 TiUSD 10 → 買 TACm（taker 0.5% 費）。
J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/TiUSD\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"1\"}" >/dev/null
R=$(J /api/exchange/flash "{\"uid\":\"$U1\",\"from\":\"TiUSD\",\"to\":\"TACm\",\"amount\":\"10\"}")
echo "$R" | grep -q '"ok":true' && ok "閃兌 TiUSD10→TACm（吃 U2 賣單）" || bad "閃兌: $R"

# 7. 先撤 U2 剩餘賣單（閃兌僅成交 0.1，剩 0.9 鎖定）→ 再提現互通。
OID=$(curl -sf "http://127.0.0.1:$RPC/api/exchange/orders?uid=$U2&market=TACM/TiUSD" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for o in d.get('orders',[]):
    if o.get('status') in ('open','partially_filled') and o.get('side')=='sell': print(o['id']); break
")
RC=$(J /api/exchange/cancel "{\"uid\":\"$U2\",\"order_id\":$OID}")
[ -n "$OID" ] && echo "$RC" | grep -q '"ok":true' && ok "撤 U2 剩餘賣單" || bad "撤單: $OID rc=$RC"
#    閃兌後 U2 ex TACm = 1.99 - 0.1（賣單成交） = 1.89 → 提現 1 → 剩 0.89。
R=$(J /api/exchange/withdraw_to_wallet "{\"uid\":\"$U2\",\"asset\":\"TACm\",\"amount\":\"1\"}")
echo "$R" | grep -q '"ok":true' && ok "提現互通 TACm1" || bad "提現: $R"
E2=$(EX "$U2")
echo "$E2" | grep -q '"avail":"0.89"' && ok "U2 交易所剩 0.89（1.99-閃兌0.1-提現1）" || bad "U2 ex: $E2"
W2B=$(W "$U2" | python3 -c "import sys,json;print(json.load(sys.stdin)['tacm_raw'])")
python3 -c "import sys;assert int('$W2B')==12*10**18,sys.exit(1)" && ok "U2 鏈上 12（11+1）" || bad "U2 鏈上: $W2B"

# 8. 獎勵池：查詢>0；topup 撮合費入池；提取回 U1。
P0=$(curl -s "http://127.0.0.1:$RPC/api/rewardpool" | python3 -c "import sys,json;print(json.load(sys.stdin)['tacm'])")
python3 -c "import sys;assert int('$P0')>0,sys.exit(1)" && ok "獎勵池有 coinbase 挹注" || bad "池: $P0"
R=$(J /api/rewardpool/topup "{\"asset\":\"TACm\",\"amount\":\"0.01\"}")
echo "$R" | grep -q '"ok":true' && ok "topup fee→獎勵池" || bad "topup: $R"
R=$(J /api/rewardpool/withdraw "{\"to\":\"$U1\",\"amount\":\"0.01\"}")
echo "$R" | grep -q '"ok":true' && ok "獎勵池提取回 U1" || bad "池提取: $R"

# 9. 聚合狀態：全子系統欄位。
R=$(curl -sf "http://127.0.0.1:$RPC/api/status")
echo "$R" | python3 -c "
import sys,json
d=json.load(sys.stdin)
assert d['ok'] and d['chain']['block_height']>0
assert d['wallet']['address']
assert d['rewardpool']['tacm']
assert 'supply' in d['tiusd']
assert d['exchange']['fee_account'] is not None
print('AGG_OK')
" | grep -q AGG_OK && ok "聚合狀態全欄位" || bad "聚合: $R"

# 10. 儀表板頁 200 + PWA 資源。
R=$(curl -s -o /tmp/e2e_dash.html -w "%{http_code}" "http://127.0.0.1:$WEB/dashboard")
[ "$R" = "200" ] && ok "儀表板 200" || bad "儀表板: $R"
grep -q '管理儀表板' /tmp/e2e_dash.html && ok "儀表板渲染" || bad "儀表板渲染"
for u in /static/manifest.webmanifest /static/sw.js /static/tacm.svg; do
  C=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$WEB$u"); [ "$C" = "200" ] && ok "PWA $u 200" || bad "PWA $u: $C"
done
curl -s "http://127.0.0.1:$WEB/exchange" | grep -c 'serviceWorker.register' | grep -q 1 && ok "exchange PWA 註冊" || bad "exchange PWA"

echo
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
