#!/usr/bin/env bash
# TAC C2C 場外交易冒煙：廣告→下單（凍結）→確認→放行→取消→申訴→守恆。
set -u
BASE=8785; WEB=8786; DATA=".smoke_c2c"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "[c2c] PASS: $*"; }
bad() { FAIL=$((FAIL+1)); echo "[c2c] FAIL: $*"; }
J()  { curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
G()  { curl -s --max-time 5 "http://127.0.0.1:$BASE$1"; }
BAL() { curl -s --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$1"; }
TACMB() { BAL "$1" | /opt/python3.12/bin/python3 -c "import sys,json;print(json.load(sys.stdin).get('tacm_raw','0'))"; }
USDTB() { BAL "$1" | /opt/python3.12/bin/python3 -c "import sys,json;print(json.load(sys.stdin).get('usdt_raw','0'))"; }
PY=/opt/python3.12/bin/python3

pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$DATA"; mkdir -p "$DATA"
nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 >"$DATA/w.log" 2>&1 &
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done

SELLER=$(curl -sf "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
BUYER=$(curl -sf "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
[ -n "$SELLER" ] && [ -n "$BUYER" ] || bad "wallet/new 失敗"
J /api/wallet/deposit "{\"to\":\"$SELLER\",\"asset\":\"USDT\",\"amount\":\"500\",\"memo\":\"c2c-seller\"}" >/dev/null
J /api/wallet/deposit "{\"to\":\"$BUYER\",\"asset\":\"TACm\",\"amount\":\"100\",\"memo\":\"c2c-buyer\"}" >/dev/null
J /api/wallet/deposit "{\"to\":\"$SELLER\",\"asset\":\"TACm\",\"amount\":\"300\",\"memo\":\"c2c-seller2\"}" >/dev/null
SB=$(TACMB "$SELLER"); BB=$(TACMB "$BUYER")

# 1. 廣告創建驗證。
A=$(J /api/c2c/ad-create "{\"address\":\"$SELLER\",\"side\":\"sell\",\"asset\":\"TACm\",\"fiat\":\"TWD\",\"price\":30,\"min_amount\":1,\"max_amount\":50,\"payment_methods\":[\"銀行轉帳\",\"LINE Pay\"]}")
echo "$A" | grep -q '"ad_id"' && ok "發佈 sell 廣告成功（TACm/TWD 30）" || bad "ad-create: $A"
B=$(J /api/c2c/ad-create "{\"address\":\"$SELLER\",\"side\":\"sell\",\"asset\":\"ETH\",\"fiat\":\"TWD\",\"price\":30,\"min_amount\":1,\"max_amount\":50,\"payment_methods\":[\"銀行轉帳\"]}")
echo "$B" | $PY -c "import sys,json;assert '不支援' in json.load(sys.stdin).get('error',''),''" 2>/dev/null \
  && ok "不支援資產（ETH）被拒" || bad "非法資產應拒: $B"
C=$(J /api/c2c/ad-create "{\"address\":\"$BUYER\",\"side\":\"buy\",\"asset\":\"USDT\",\"fiat\":\"TWD\",\"price\":31.5,\"min_amount\":5,\"max_amount\":100,\"payment_methods\":[\"街口支付\"]}")
echo "$C" | grep -q '"ad_id"' && ok "發佈 buy 廣告成功（USDT/TWD 31.5）" || bad "buy ad-create: $C"

# 2. 廣告列表（價格升序、過濾）。
L=$(G "/api/c2c/ads?asset=TACm&fiat=TWD&side=sell")
echo "$L" | $PY -c "import sys,json;d=json.load(sys.stdin);ads=d['ads'];assert len(ads)==1 and ads[0]['address']=='$SELLER' and ads[0]['price']==30" \
  && ok "廣告列表過濾（TACm/TWD/sell）正確" || bad "ads: $L"
L2=$(G "/api/c2c/ads?asset=USDT&fiat=TWD")
echo "$L2" | $PY -c "import sys,json;d=json.load(sys.stdin);ads=d['ads'];assert len(ads)==1 and ads[0]['side']=='buy'" \
  && ok "buy 廣告列表正確" || bad "ads2: $L2"

# 3. 下單：sell 廣告 → 賣家幣凍結入 escrow。
O=$(J /api/c2c/order-create "{\"ad_id\":1,\"buyer\":\"$BUYER\",\"amount\":10,\"payment_method\":\"銀行轉帳\"}")
echo "$O" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d.get('ok') and d['total_fiat']==300.0, d" \
  && ok "下單成功（10 TACm × 30 = 300 TWD）" || bad "order-create: $O"
SAFTER=$(TACMB "$SELLER")
$PY -c "import sys;assert int('$SB')-int('$SAFTER') == int(10*10**18*1.02), (int('$SB')-int('$SAFTER'), int(10*10**18*1.02))" 2>/dev/null \
  && ok "賣家 TACm 已扣 10＋鏈上費（擔保凍結）" || bad "凍結扣款異常: $SB -> $SAFTER"
ES=$(BAL c2c_escrow | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm_raw','0'))")
$PY -c "import sys;assert int('$ES')>=int(10*10**18*0.98), '$ES'" 2>/dev/null \
  && ok "escrow 擔保帳戶凍結 ≥10 TACm（實際 $ES wei）" || bad "escrow: $ES"
O2=$(J /api/c2c/order-create "{\"ad_id\":1,\"buyer\":\"$BUYER\",\"amount\":100,\"payment_method\":\"銀行轉帳\"}")
echo "$O2" | $PY -c "import sys,json;assert '必須在' in json.load(sys.stdin).get('error',''),''" 2>/dev/null \
  && ok "超出廣告限額（100>50）被拒" || bad "超額應拒: $O2"
O3=$(J /api/c2c/order-create "{\"ad_id\":1,\"buyer\":\"$SELLER\",\"amount\":5,\"payment_method\":\"銀行轉帳\"}")
echo "$O3" | $PY -c "import sys,json;assert '不能與自己交易' in json.load(sys.stdin).get('error',''),''" 2>/dev/null \
  && ok "自己與自己交易被拒" || bad "自己交易應拒: $O3"

# 4. 確認付款（買家）→ 放行（賣家）。
MO=$(G "/api/c2c/my-orders?address=$BUYER")
echo "$MO" | $PY -c "import sys,json;d=json.load(sys.stdin);assert len(d['orders'])==1 and d['orders'][0]['status']=='pending'" \
  && ok "我的訂單（pending，買家視角）" || bad "my-orders: $MO"
CF=$(J /api/c2c/order-confirm "{\"order_id\":1,\"address\":\"$BUYER\"}")
echo "$CF" | grep -q '"ok":true' && ok "買家確認付款（pending→paid）" || bad "confirm: $CF"
CFB=$(J /api/c2c/order-confirm "{\"order_id\":1,\"address\":\"$SELLER\"}")
echo "$CFB" | $PY -c "import sys,json;assert '買家' in json.load(sys.stdin).get('error',''),''" 2>/dev/null \
  && ok "非買家確認付款被拒" || bad "非買家應拒: $CFB"
REL=$(J /api/c2c/order-release "{\"order_id\":1,\"address\":\"$SELLER\"}")
echo "$REL" | grep -q '"ok":true' && ok "賣家放行（escrow→買家）" || bad "release: $REL"
BB2=$(TACMB "$BUYER")
$PY -c "import sys;assert int('$BB2')-int('$BB') == int(10*10**18), (int('$BB2')-int('$BB'))" 2>/dev/null \
  && ok "買家收到 10 TACm（放行免二次費）" || bad "買家收款異常: $BB -> $BB2"
ES2=$(BAL c2c_escrow | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm_raw','0'))")
[ "$ES2" = "0" ] && ok "escrow 清空（全部釋放）" || bad "escrow 應空: $ES2"
MO2=$(G "/api/c2c/my-orders?address=$BUYER")
echo "$MO2" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['orders'][0]['status']=='completed'" \
  && ok "訂單狀態 completed" || bad "completed 異常: $MO2"
MA=$(G "/api/c2c/my-ads?address=$SELLER")
echo "$MA" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['ads'][0]['completed_orders']==1" \
  && ok "廣告完成數 +1" || bad "completed_orders: $MA"

# 5. 取消（buy 廣告下單後取消，無凍結）。
O4=$(J /api/c2c/order-create "{\"ad_id\":2,\"buyer\":\"$SELLER\",\"amount\":5,\"payment_method\":\"街口支付\"}")
echo "$O4" | grep -q '"order_id"' && ok "buy 廣告下單成功（無凍結）" || bad "buy order: $O4"
CAN=$(J /api/c2c/order-cancel "{\"order_id\":2,\"address\":\"$BUYER\"}")
echo "$CAN" | grep -q '"ok":true' && ok "買家取消訂單（pending→cancelled）" || bad "cancel: $CAN"
CAN2=$(J /api/c2c/order-cancel "{\"order_id\":2,\"address\":\"$BUYER\"}")
echo "$CAN2" | $PY -c "import sys,json;assert '狀態' in json.load(sys.stdin).get('error',''),''" 2>/dev/null \
  && ok "已取消訂單不可再取消" || bad "二次取消應拒: $CAN2"

# 6. 申訴。
O5=$(J /api/c2c/order-create "{\"ad_id\":1,\"buyer\":\"$BUYER\",\"amount\":5,\"payment_method\":\"銀行轉帳\"}")
DSP=$(J /api/c2c/order-dispute "{\"order_id\":3,\"address\":\"$BUYER\",\"reason\":\"賣家未回應\"}")
echo "$DSP" | grep -q '"ok":true' && ok "申訴成立（pending→disputed）" || bad "dispute: $DSP"
DSP2=$(J /api/c2c/order-dispute "{\"order_id\":1,\"address\":\"$BUYER\",\"reason\":\"x\"}")
echo "$DSP2" | $PY -c "import sys,json;assert '狀態' in json.load(sys.stdin).get('error',''),''" 2>/dev/null \
  && ok "已完成訂單不可申訴" || bad "已完成應拒: $DSP2"

# 7. 守恆：申訴單擔保保留＋賣家已精確扣費。
ESD=$(BAL c2c_escrow | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm_raw','0'))")
[ "$ESD" = "5000000000000000000" ] && ok "申訴單 escrow 保留 5 TACm（擔保未釋放）" || bad "escrow 申訴保留: $ESD"
SF=$(TACMB "$SELLER")
$PY -c "import sys;assert int('$SF') <= int((300-15.3)*10**18), '$SF'" 2>/dev/null   && ok "賣家資產已扣凍結額含費（≤284.7 TACm，實際 $SF wei）" || bad "賣家扣款不足: $SF"

# 8. /c2c 頁面。
H=$(curl -sf --max-time 5 "http://127.0.0.1:$WEB/c2c")
echo "$H" | grep -q 'C2C 場外交易' && ok "/c2c 頁面可達（幣安風 C2C）" || bad "/c2c 頁面異常"

pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null
echo "================ 結果：PASS=$PASS FAIL=$FAIL ================"
[ "$FAIL" = "0" ]
