#!/usr/bin/env bash
# TAC 自主智能鏈 M14 交易所冒煙測試：真實進程 + HTTP API 全流程。
# 啟動單節點（BASE 埠 8740）→ 入金 → 限價撮合 → 市價單 → 撤單 →
# 閃兌 → 自動交易機器人 → 停止機器人。PASS/FAIL 計數。
set -u
BASE=8740
ROOT="$(cd "$(dirname "$0")" && pwd)"
BIN="$ROOT/tacnode"
DATA="$ROOT/.smoke_ex"
PASS=0; FAIL=0

log()  { echo "[smoke] $*"; }
ok()   { PASS=$((PASS+1)); log "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); log "FAIL: $1"; }

pkill -x tacnode 2>/dev/null; sleep 0.4
rm -rf "$DATA"

# 1. 編譯（若無二進位）。
[ -x "$BIN" ] || (cd "$ROOT" && GOTOOLCHAIN=local go build -o tacnode ./cmd/node) || { echo "build failed"; exit 2; }

# 2. 啟動節點。
"$BIN" -init-identity -data-dir "$DATA" >/dev/null 2>&1
"$BIN" -data-dir "$DATA" -port $BASE -block-time 1 -difficulty 1 >"$DATA/node.log" 2>&1 &
NODE_PID=$!
trap 'kill $NODE_PID 2>/dev/null; pkill -x tacnode 2>/dev/null' EXIT
for i in $(seq 1 20); do
  curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break
  sleep 0.3
done
curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && ok "節點啟動" || bad "節點啟動"

U1="tx0SmokeA"; U2="tx0SmokeB"
J() { curl -sf -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }

# 3. 入金：U1 入 10 TACM + 1000 USDT；U2 入 1000 USDT。
J /api/exchange/deposit "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"10\"}" >/dev/null && ok "入金 TACm" || bad "入金 TACm"
J /api/exchange/deposit "{\"uid\":\"$U1\",\"asset\":\"USDT\",\"amount\":\"1000\"}" >/dev/null && ok "入金 USDT(U1)" || bad "入金 USDT(U1)"
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"USDT\",\"amount\":\"1000\"}" >/dev/null && ok "入金 USDT(U2)" || bad "入金 USDT(U2)"
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"TACm\",\"amount\":\"10\"}" >/dev/null && ok "入金 TACm(U2)" || bad "入金 TACm(U2)"

# 4. U2 掛賣 2 TACM @ 500 USDT。
R=$(J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/USDT\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"500\",\"qty\":\"2\"}")
echo "$R" | grep -q '"ok":true' && ok "掛賣單" || bad "掛賣單: $R"

# 5. U1 限價買 1 TACM @ 500 → 成交 1 筆。
R=$(J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/USDT\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"500\",\"qty\":\"1\"}")
echo "$R" | grep -q '"status":"filled"' && ok "限價買成交" || bad "限價買成交: $R"

# 7. 市價買 0.5 TACM。
R=$(J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/USDT\",\"side\":\"buy\",\"type\":\"market\",\"price\":\"0\",\"qty\":\"0.5\"}")
echo "$R" | grep -q '"ok":true' && ok "市價買成交" || bad "市價買: $R"

# 7.5 餘額驗證：U1 TACM = 10 + 0.9875 + 0.49375 = 11.48125。
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/balances?uid=$U1")
echo "$R" | grep -q '"avail":"11.48125"' && ok "U1 TACM 11.48125" || bad "U1 TACM: $R"

# 8. 訂單簿查詢。
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/book?market=TACM/USDT")
echo "$R" | grep -q '"asks"' && ok "訂單簿查詢" || bad "訂單簿: $R"

# 8.5 提供閃兌 bid 深度（賣 TACM 吃買單）：U3（第三方）入金 USDT 並掛買單 2 @ 500。
U3="tx0SmokeC"
J /api/exchange/deposit "{\"uid\":\"$U3\",\"asset\":\"USDT\",\"amount\":\"1000\"}" >/dev/null
R=$(J /api/exchange/order "{\"uid\":\"$U3\",\"market\":\"TACM/USDT\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"500\",\"qty\":\"2\"}")
echo "$R" | grep -q '"ok":true' && ok "掛買單(閃兌深度)" || bad "掛買單(閃兌深度): $R"

# 9. 閃兌：U1 賣 1 TACM → USDT。
R=$(J /api/exchange/flash "{\"uid\":\"$U1\",\"from\":\"TACm\",\"to\":\"USDT\",\"amount\":\"1\"}")
echo "$R" | grep -q '"ok":true' && ok "閃兌執行" || bad "閃兌: $R"

# 10. 自動交易機器人：U1 啟動（TACM/USDT 雙側掛單）。
R=$(J /api/exchange/bot/start "{\"uid\":\"$U1\",\"market\":\"TACM/USDT\",\"mid_price\":\"500\",\"spread_pct\":20,\"qty\":\"0.1\",\"interval_sec\":1,\"cancel_on_refresh\":true}")
BID=$(echo "$R" | sed -n 's/.*"bot_id":"\([^"]*\)".*/\1/p')
[ -n "$BID" ] && ok "機器人啟動 $BID" || bad "機器人啟動: $R"
sleep 1.3
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/bots")
echo "$R" | grep -q '"status":"running"' && ok "機器人運行中" || bad "機器人狀態: $R"

# 11. 停止機器人。
R=$(J /api/exchange/bot/stop "{\"id\":\"$BID\"}")
echo "$R" | grep -q '"status":"stopped"' && ok "機器人停止" || bad "機器人停止: $R"

# 12. 撤單：U3 閃兌深度剩餘買單（partially_filled）撤銷（經由「我的訂單」API 取 OID）。
ORDERS=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/orders?uid=$U3&market=TACM/USDT")
OID=$(echo "$ORDERS" | /opt/python3.12/bin/python3 -c 'import sys,json; d=json.load(sys.stdin); print(next((o["id"] for o in d["orders"] if o["side"]=="buy" and o["status"] in ("open","partially_filled")), ""))' 2>/dev/null)
[ -z "$OID" ] && log "orders 回應: $ORDERS"
if [ -n "$OID" ]; then
  R=$(J /api/exchange/cancel "{\"uid\":\"$U3\",\"order_id\":$OID}")
  echo "$R" | grep -q '"ok":true' && ok "撤單" || bad "撤單: $R"
else
  bad "找不到可撤訂單"
fi

echo
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
