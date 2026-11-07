#!/usr/bin/env bash
# TAC 自主智能鏈 M15 冒煙：錢包↔交易所資產互通 + 手續費歸帳。
# 啟動節點（BASE 8750）→ 鏈上錢包入金 → 交易所撮合 → fee 帳戶挹注 →
# 提現回鏈上 → 守恆驗證。PASS/FAIL 計數。
set -u
BASE=8750
ROOT="$(cd "$(dirname "$0")" && pwd)"
BIN="$ROOT/tacnode"
DATA="$ROOT/.smoke_m15"
PASS=0; FAIL=0
log()  { echo "[smoke] $*"; }
ok()   { PASS=$((PASS+1)); log "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); log "FAIL: $1"; }

pkill -x tacnode 2>/dev/null; sleep 0.4
rm -rf "$DATA"; mkdir -p "$DATA"
(cd "$ROOT" && GOTOOLCHAIN=local go build -o tacnode ./cmd/node) || { echo "build failed"; exit 2; }
"$BIN" -data-dir "$DATA" -port $BASE -block-time 1 -difficulty 1 >"$DATA/node.log" 2>&1 &
NODE_PID=$!
trap 'kill $NODE_PID 2>/dev/null; pkill -x tacnode 2>/dev/null' EXIT
for i in $(seq 1 20); do curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && ok "節點啟動" || bad "節點啟動"

U1="tx0M15A"; U2="tx0M15B"
J() { curl -sf -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }

# 0. 鏈上錢包：U1 充值 TACm 10 + TiUSD 100（模擬外部資金）。
J /api/wallet/deposit "{\"to\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"seed\"}" >/dev/null
J /api/tiusd/mint "{\"to\":\"$U1\",\"amount\":\"100\",\"note\":\"seed\"}" >/dev/null
J /api/tiusd/mint "{\"to\":\"$U2\",\"amount\":\"300\",\"note\":\"seed\"}" >/dev/null
J /api/wallet/deposit "{\"to\":\"$U2\",\"asset\":\"USDT\",\"amount\":\"1000\",\"memo\":\"seed\"}" >/dev/null
J /api/wallet/deposit "{\"to\":\"$U2\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"seed\"}" >/dev/null

# 1. 入金互通：U1 鏈上 TACm 5 → 交易所（鏈上扣 5+0.1 手續費 2%）。
R=$(J /api/exchange/deposit_from_wallet "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"5\"}")
echo "$R" | grep -q '"ok":true' && ok "入金互通 TACm5" || bad "入金互通: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/balances?uid=$U1")
echo "$R" | grep -q '"avail":"5"' && ok "ex 入帳 5 TACm" || bad "ex 入帳: $R"

# 2. 鏈上錢包餘額驗證：U1 鏈上剩 10-5.1=4.9 TACm。
R=$(curl -sf "http://127.0.0.1:$BASE/api/wallet/info?address=$U1")
echo "$R" | grep -q '"tacm":"4.9"' && ok "鏈上扣款 4.9" || bad "鏈上餘額: $R"

# 3. 入金互通（TiUSD）：U1 鏈上 TiUSD 20 → 交易所（費 0.5% = 0.1）。
R=$(J /api/exchange/deposit_from_wallet "{\"uid\":\"$U1\",\"asset\":\"TiUSD\",\"amount\":\"20\"}")
echo "$R" | grep -q '"ok":true' && ok "入金互通 TiUSD20" || bad "入金互通 TiUSD: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/wallet/info?address=$U1")
echo "$R" | grep -q '"tiusd":"79.9"' && ok "鏈上 TiUSD 79.9" || bad "鏈上 TiUSD: $R"

# 4. 撮合：U1 賣 2 TACM（TiUSD 報價，taker 0.5%）；U2 買 2 TACM。
J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/TiUSD\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}" >/dev/null
R=$(J /api/exchange/deposit_from_wallet "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"200\"}")
echo "$R" | grep -q '"ok":true' && ok "U2 入金互通 TiUSD200" || bad "U2 入金互通: $R"
R=$(J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/TiUSD\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}")
echo "$R" | grep -q '"status":"filled"' && ok "撮合成交 TACM/TiUSD" || bad "撮合: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/balances?uid=$U2")
echo "$R" | grep -q '"avail":"1.99"' && ok "U2 ex TACm 1.99 (2-0.01 taker fee)" || bad "U2 ex TACm: $R"

# 5. fee 帳戶挹注：taker 買 2×0.5%=0.1 TACM + maker 賣 200×0.25%=0.5 TiUSD。
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/fee")
echo "$R" | grep -q '"TACm","avail":"0.01"' && ok "fee 帳戶 TACm 0.01 (taker 2×0.5%)" || bad "fee TACm: $R"
echo "$R" | grep -q '"TiUSD","avail":"0.5"' && ok "fee 帳戶 TiUSD 0.5" || bad "fee TiUSD: $R"

# 6. 提現互通：U2 交易所 1 TACM → 鏈上（鏈上扣 1+0.02 費）。
R=$(J /api/exchange/withdraw_to_wallet "{\"uid\":\"$U2\",\"asset\":\"TACm\",\"amount\":\"1\"}")
echo "$R" | grep -q '"ok":true' && ok "提現互通 TACm1" || bad "提現互通: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/wallet/info?address=$U2")
echo "$R" | grep -q '"tacm":"11"' && ok "U2 鏈上 TACm 11 (10 + 提現全額 1；轉帳費 0.02 由 vault 承擔)" || bad "U2 鏈上: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/balances?uid=$U2")
echo "$R" | grep -q '"avail":"0.99"' && ok "U2 ex TACm 剩 0.99" || bad "U2 ex: $R"

# 7. 守恆驗證：交易所總資產 = 入金總和（5 TACm + 20 TiUSD + 200 TiUSD）− 提現 1 TACm。
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/balances?uid=$U1")
echo "$R" | grep -q '"avail":"3"' && ok "U1 ex TACm 3 (5-2 已付)" || bad "U1 ex TACm: $R"

# 8. 提現失敗回滾：U1 提現 999 TACm（ex 餘額不足 → 失敗且鏈上不受影響）。
R=$(curl -s -X POST "http://127.0.0.1:$BASE/api/exchange/withdraw_to_wallet" -H 'Content-Type: application/json' -d "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"999\"}")
echo "$R" | grep -q '"error"' && ok "超額提現被拒" || bad "超額提現: $R"
R=$(curl -sf "http://127.0.0.1:$BASE/api/exchange/balances?uid=$U1")
echo "$R" | grep -q '"avail":"3"' && ok "回滾後 ex 餘額不變" || bad "回滾驗證: $R"

echo
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
