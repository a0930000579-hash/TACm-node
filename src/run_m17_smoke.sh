#!/usr/bin/env bash
# TAC 自主智能鏈 M17 冒煙：聚合狀態 RPC + 管理儀表板。
set -u
BASE=8770
ROOT="$(cd "$(dirname "$0")" && pwd)"
DATA="$ROOT/.smoke_m17"
PASS=0; FAIL=0
log()  { echo "[smoke] $*"; }
ok()   { PASS=$((PASS+1)); log "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); log "FAIL: $1"; }

pkill -x tacnode 2>/dev/null; pkill -x tacweb 2>/dev/null; sleep 0.4
rm -rf "$DATA"; mkdir -p "$DATA"
(cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web) || { echo "build failed"; exit 2; }
nohup ./tacweb -data-dir "$DATA" -web-port $((BASE-1)) -rpc-port $BASE -block-time 1 -difficulty 1 >"$DATA/web.log" 2>&1 &
WPID=$!
trap 'kill $WPID 2>/dev/null; pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null' EXIT
for i in $(seq 1 20); do curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && ok "節點啟動" || bad "節點啟動"

# 0. 等出塊（coinbase → 錢包 + 獎勵池）。
sleep 2.5

# 1. 聚合狀態 RPC：所有子系統欄位齊全。
R=$(curl -sf "http://127.0.0.1:$BASE/api/status")
echo "$R" | python3 -c "
import sys,json
d=json.load(sys.stdin)
assert d.get('ok') is True, 'ok=false'
c=d.get('chain',{}); assert c.get('block_height',0)>0, 'chain.height=0'
assert 'address' in d.get('wallet',{}), 'wallet missing'
assert d.get('rewardpool',{}).get('tacm'), 'rewardpool empty'
assert 'supply' in d.get('tiusd',{}), 'tiusd missing'
assert 'fee_account' in d.get('exchange',{}), 'exchange missing'
print('AGG_OK')
" | grep -q AGG_OK && ok "聚合狀態 RPC 全欄位" || bad "聚合狀態: $R"

# 2. 出塊後鏈上錢包 TACm > 0（coinbase 85%）。
R=$(curl -sf "http://127.0.0.1:$BASE/api/status")
echo "$R" | python3 -c "
import sys,json
d=json.load(sys.stdin)
w=d['wallet']; assert float(w.get('tacm','0') or 0) > 0, 'wallet tacm empty'
r=d['rewardpool']; assert float(r.get('tacm','0') or 0) > 0, 'rewardpool empty'
print('FUND_OK')
" | grep -q FUND_OK && ok "錢包＋獎勵池有實質資產" || bad "資產: $R"

# 3. 產生交易所活動：入金互通 + 撮合 → fee 帳戶有值。
U1="tx0M17A"; U2="tx0M17B"
J() { curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
J /api/wallet/deposit "{\"to\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"s\"}" >/dev/null
# M31：TiUSD 禁止私自鑄造（公開 mint 已移除）——改由交易所資金池直接入金（後台結算等效）。
J /api/exchange/deposit "{\"uid\":\"$U1\",\"asset\":\"TiUSD\",\"amount\":\"500\"}" >/dev/null
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"500\"}" >/dev/null
J /api/exchange/deposit_from_wallet "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"5\"}" >/dev/null
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"300\"}" >/dev/null
J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/TiUSD\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}" >/dev/null
R=$(J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/TiUSD\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}")
echo "$R" | grep -q '"status":"filled"' && ok "撮合成交" || bad "撮合: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/status")
echo "$R" | python3 -c "
import sys,json
d=json.load(sys.stdin)
fees=d.get('exchange',{}).get('fee_account',[])
assert any(float(f.get('avail','0') or 0)>0 for f in fees), 'fee account empty after trade'
print('FEE_OK')
" | grep -q FEE_OK && ok "聚合狀態反映 fee 帳戶" || bad "fee 聚合: $R"

# 4. Web 儀表板：200 + 卡片渲染。
R=$(curl -sf -o /tmp/m17_dash.html -w "%{http_code}" "http://127.0.0.1:$((BASE-1))/dashboard")
[ "$R" = "200" ] && ok "儀表板頁 200" || bad "儀表板: $R"
grep -q '管理儀表板' /tmp/m17_dash.html && ok "儀表板模板渲染" || bad "儀表板模板"
grep -q '獎勵池' /tmp/m17_dash.html && ok "儀表板含獎勵池卡" || bad "儀表板卡片"

echo
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
