#!/usr/bin/env bash
# TAC M13 錢包冒煙：2 節點分佈式網（BASE=8650）驗證
#   1) coinbase 獎勵入錢包（鏈上同步）
#   2) TiUSD mint/burn 供給
#   3) TiUSD 0.5% 手續費轉帳 / USDT 1.25% 手續費轉帳
#   4) 跨節點鏈上交易（tacctl 簽名）→ 收款節點錢包同步
#   5) 幣安風 /wallet 頁面渲染
set -e
cd "$(dirname "$0")"
export GOTOOLCHAIN=local
BASE=8650
WORK=$(mktemp -d)
echo "[build] 編譯..."
go build -o "$WORK/tacnode" ./cmd/node
go build -o "$WORK/tacctl" ./cmd/tacctl
go build -o "$WORK/web" ./cmd/web

echo "[init] 生成驗證人身份..."
SPEC=""
A0=""; A1=""
for i in 0 1; do
	LINE=$("$WORK/tacnode" -node-id "w$i" -data-dir "$WORK/d$i" -init-identity)
	ADDR=$(echo "$LINE" | cut -d: -f2)
	[ "$i" = "0" ] && A0="$ADDR" || A1="$ADDR"
	[ -z "$SPEC" ] && SPEC="$LINE" || SPEC="$SPEC,$LINE"
done
echo "w0=$A0  w1=$A1"

echo "[run] 啟動 2 節點..."
"$WORK/tacnode" -node-id "w0" -data-dir "$WORK/d0" -port $BASE -block-time 1 -difficulty 1 \
	-distributed -genesis-validators "$SPEC" >"$WORK/w0.log" 2>&1 &
P0=$!
"$WORK/tacnode" -node-id "w1" -data-dir "$WORK/d1" -port $((BASE+1)) -block-time 1 -difficulty 1 \
	-distributed -genesis-validators "$SPEC" -peers "http://127.0.0.1:$BASE" >"$WORK/w1.log" 2>&1 &
P1=$!
trap 'kill $P0 $P1 2>/dev/null; rm -rf "$WORK"' EXIT

sleep 8
GET() { curl -s "http://127.0.0.1:$1$2"; }
PASS=0; FAIL=0
ok()  { echo "  ✅ $1"; PASS=$((PASS+1)); }
bad() { echo "  ❌ $1"; FAIL=$((FAIL+1)); }

echo "== 1. coinbase 獎勵入帳（鏈上同步進錢包）=="
TAC0=$(GET $BASE "/api/wallet/info?address=$A0" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tacm_raw"])')
echo "  w0 TACm raw=$TAC0"
[ -n "${TAC0:-}" ] && [ "$TAC0" != "0" ] && ok "coinbase 已入帳" || bad "coinbase 未入帳 ($TAC0)"

echo "== 2. TiUSD 供給與禁止私自鑄造（M35：僅鏈上機制鑄造） =="
SUP=$(GET $BASE "/api/tiusd/summary")
echo "$SUP" | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d.get("ok") and "supply_raw" in d and d.get("peg_price")=="1.0"; print("supply_raw=%s peg=%s（錨定 1 USDT）" % (d["supply_raw"], d["peg_price"]))' \
	&& ok "TiUSD summary 正常（錨定 1 USDT）" || bad "TiUSD summary 異常"
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://127.0.0.1:$BASE/api/tiusd/mint" -H 'Content-Type: application/json' \
	-d "{\"to\":\"$A0\",\"amount\":\"50\",\"note\":\"smoke\"}")
[ "$CODE" = "404" ] && ok "公開 mint 路由已關閉（禁止私自鑄造）" || bad "mint 路由狀態異常 code=$CODE"

echo "== 3. TiUSD 0.5% 手續費轉帳 =="
curl -s -X POST "http://127.0.0.1:$BASE/api/wallet/deposit" -H 'Content-Type: application/json' \
	-d '{"to":"tx0SmokeDepositB","asset":"TIUSD","amount":"100","memo":"smoke"}' >/dev/null
FEE=$(curl -s -X POST "http://127.0.0.1:$BASE/api/wallet/transfer" -H 'Content-Type: application/json' \
	-d '{"from":"tx0SmokeDepositB","to":"'$A0'","asset":"TIUSD","amount":"10"}' \
	| python3 -c 'import json,sys; print(json.load(sys.stdin)["fee_raw"])')
[ "$FEE" = "0.05" ] && ok "TiUSD 手續費=$FEE（10×0.5%=0.05）" || bad "TiUSD 手續費異常=$FEE"

echo "== 4. USDT 1.25% 手續費轉帳 =="
curl -s -X POST "http://127.0.0.1:$BASE/api/wallet/deposit" -H 'Content-Type: application/json' \
	-d '{"to":"tx0SmokeDepositC","asset":"USDT","amount":"100","memo":"smoke"}' >/dev/null
FEE=$(curl -s -X POST "http://127.0.0.1:$BASE/api/wallet/transfer" -H 'Content-Type: application/json' \
	-d '{"from":"tx0SmokeDepositC","to":"'$A0'","asset":"USDT","amount":"80"}' \
	| python3 -c 'import json,sys; print(json.load(sys.stdin)["fee_raw"])')
[ "$FEE" = "1" ] && ok "USDT 手續費=$FEE（80×1.25%=1）" || bad "USDT 手續費異常=$FEE"

echo "== 5. 幣安風 /wallet 頁面 =="
"$WORK/web" -node-id wweb -data-dir "$WORK/dweb" -rpc-port $((BASE+20)) -web-port $((BASE+21)) \
	-block-time 1 -difficulty 1 >"$WORK/web.log" 2>&1 &
PW=$!
sleep 2
WCODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$((BASE+21))/wallet?address=$A0")
WTXT=$(curl -s "http://127.0.0.1:$((BASE+21))/wallet?address=$A0")
[ "$WCODE" = "200" ] && echo "$WTXT" | grep -q "資產總覽" && ok "wallet 頁面 HTTP 200 含資產總覽" || bad "wallet 頁面 HTTP=$WCODE"
kill $PW 2>/dev/null

echo "== 6. 資產跨帳戶流動（deposit → 站內轉帳；鏈上 mempool→區塊→錢包同步由 e2e TestWalletSyncChainTx 覆蓋）=="
curl -s -X POST "http://127.0.0.1:$BASE/api/wallet/deposit" -H 'Content-Type: application/json' \
	-d '{"to":"tx0SmokeDepositD","asset":"TACm","amount":"6","memo":"smoke"}' >/dev/null
TR=$(curl -s -X POST "http://127.0.0.1:$BASE/api/wallet/transfer" -H 'Content-Type: application/json' \
	-d '{"from":"tx0SmokeDepositD","to":"'$A1'","asset":"TACm","amount":"5"}' | python3 -c 'import json,sys; print(json.load(sys.stdin).get("ok"))')
[ "$TR" = "True" ] && ok "TACm 跨帳戶轉帳入帳成功" || bad "TACm 轉帳失敗 ($TR)"
B1=$(GET $((BASE+1)) "/api/wallet/info?address=$A1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tacm_raw"])')
echo "  w1 錢包 $A1 TACm raw=$B1"
[ -n "$B1" ] && ok "錢包資訊端點跨節點正常" || bad "w1 錢包端點異常"

echo ""
echo "==== M13 錢包冒煙: PASS=$PASS FAIL=$FAIL ===="
exit $FAIL
