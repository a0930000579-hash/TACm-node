#!/usr/bin/env bash
# TAC 自主智能鏈 M21-C 社群實機驗證（搬運自原本 community_app 並 App 化）：
#   發文/按讚/留言 → feed 計數 → 市集(buy-intent) → 廣告(付款建議) → 快捷入金交易所 → 頁面渲染。
set -u
BASE=8781; WEB=8782; DATA=".smoke_cm"
ROOT="$(cd "$(dirname "$0")" && pwd)"; cd "$ROOT"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "[cm] PASS: $*"; }
bad() { FAIL=$((FAIL+1)); echo "[cm] FAIL: $*"; }
log() { echo "[cm] $*"; }
PY=/opt/python3.12/bin/python3
J() { curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }

pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$DATA" && mkdir -p "$DATA"
( cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web ) || { echo "build failed"; exit 2; }
nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 >"$DATA/w.log" 2>&1 &
trap 'pkill -x tacweb 2>/dev/null' EXIT
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done
sleep 0.5

# 身份：A＝節點地址（默認）、B＝新地址（預充值）。
A=$(curl -sf "http://127.0.0.1:$BASE/status" | $PY -c "import sys,json;print(json.load(sys.stdin)['address'])")
B=$(curl -sf "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
J /api/wallet/deposit "{\"to\":\"$B\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"cm\"}" >/dev/null
[ -n "$B" ] && ok "身份 B 產生（${B:0:12}…）" || bad "wallet/new 失敗"

# 1. A 發動態 + B 發市集商品。
R=$(J /api/community/post "{\"address\":\"$A\",\"kind\":\"post\",\"content\":\"測試動態：TAC 自主智能鏈挖礦日記\"}")
PID=$(echo "$R" | $PY -c "import sys,json;print(json.load(sys.stdin).get('post_id',0))" 2>/dev/null)
[ "${PID:-0}" -gt 0 ] && ok "A 發文成功（post_id=$PID）" || bad "發文: $R"
R=$(J /api/community/post "{\"address\":\"$B\",\"kind\":\"market\",\"content\":\"測試礦機\n八成新\",\"price_tacm\":5}")
MID=$(echo "$R" | $PY -c "import sys,json;print(json.load(sys.stdin).get('post_id',0))" 2>/dev/null)
[ "${MID:-0}" -gt 0 ] && ok "B 發市集商品成功（item_id=$MID, 5 TACM）" || bad "市集發佈: $R"
R=$(J /api/community/post "{\"address\":\"$B\",\"kind\":\"post\",\"content\":\"第二篇動態\"}")
[ "$(echo "$R" | $PY -c "import sys,json;print(json.load(sys.stdin).get('post_id',0))" 2>/dev/null)" -gt 0 ] && ok "B 發文成功" || bad "B 發文: $R"

# 2. B 按讚 + B 留言 A 的貼文。
J /api/community/post/$PID/like "{\"address\":\"$B\"}" >/dev/null
J /api/community/post/$PID/like "{\"address\":\"$B\"}" >/dev/null
R=$(J /api/community/post/$PID/comments "{\"address\":\"$B\",\"body\":\"讚！自主鏈 V 幾？\"}")
echo "$R" | grep -q '"ok":true' && ok "B 按讚＋留言成功（按讚冪等）" || bad "按讚/留言: $R"

# 3. feed：最新在前、A 貼文 likes=1 comments=1。
FEED=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/community/feed")
N=$(echo "$FEED" | $PY -c "import sys,json;print(len(json.load(sys.stdin).get('items',[])))")
echo "$FEED" | grep -q '"likes":1' && echo "$FEED" | grep -q '"comments":1' && [ "$N" -ge 3 ] && ok "動態牆：$N 篇、A 貼文 likes=1/comments=1" || bad "feed 異常（$N 篇）: $(echo "$FEED" | head -c 160)"
# 4. 市集列表＋buy-intent：A 買 B 的商品（賣家=B）。
MKT=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/community/market")
echo "$MKT" | grep -q '"id":'"$MID"'' && echo "$MKT" | grep -q '"price_tacm":5' && ok "市集含商品 #$MID（5 TACM）" || bad "市集: $(echo "$MKT" | head -c 160)"
R=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/community/market/$MID/buy-intent?address=$A")
echo "$R" | grep -q '"to":"'"$B"'"' && echo "$R" | grep -q '"amount_tacm":5' && echo "$R" | grep -q '"memo":"market#'"$MID"':buyer:'"$A"'"' && ok "buy-intent：賣家=B、金額=5 TACM、memo 正確" || bad "buy-intent: $R"
# 4b. 市集一鍵購買閉環：C 買 B 商品（5 TACM，鏈上費 2% 入資金池）。
C=$(curl -sf "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
J /api/wallet/deposit "{\"to\":\"$C\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"cm-buy\"}" >/dev/null
B0=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$B" | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm','0'))" 2>/dev/null)
P0=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/pool" | $PY -c "import sys,json;d=json.load(sys.stdin);a=d.get('assets',[]);print([x.get('balance','0') for x in a if x.get('asset')=='TACm'][0] if a else '0')" 2>/dev/null)
R=$(J /api/community/market/$MID/buy "{\"address\":\"$C\"}")
echo "$R" | grep -q '"status":"paid"' && echo "$R" | grep -q '"amount_tacm":5' && ok "C 一鍵購買成功（status=paid、5 TACM）" || bad "buy 執行: $R"
CB=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$C" | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm','0'))" 2>/dev/null)
B1=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$B" | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm','0'))" 2>/dev/null)
P1=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/pool" | $PY -c "import sys,json;d=json.load(sys.stdin);a=d.get('assets',[]);print([x.get('balance','0') for x in a if x.get('asset')=='TACm'][0] if a else '0')" 2>/dev/null)
$PY -c "
import sys
cb, b0, b1, p0, p1 = float('$CB'), float('$B0'), float('$B1'), float('$P0'), float('$P1')
assert 4.85 <= cb <= 4.95, 'C 餘額 %s'%cb
assert abs((b1-b0)-5.0) < 0.01, '賣家入帳 %s->%s'%(b0,b1)
assert (p1-p0) >= 0.09, '池入帳 %s->%s'%(p0,p1)
print(1)" 2>/dev/null && ok "閉環守恆：C 餘 ${CB}、賣家 ${B0}→${B1}（+5）、資金池 ${P0}→${P1}（+費）" || bad "閉環守恆異常: C=$CB B=$B0->$B1 P=$P0->$P1"
C=''
# 4c. 重複購買防護：D 再買已售商品 → 409；市集列表標記 sold。
D=$(curl -sf "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
J /api/wallet/deposit "{\"to\":\"$D\",\"asset\":\"TACm\",\"amount\":\"5\",\"memo\":\"cm-d\"}" >/dev/null
R=$(J /api/community/market/$MID/buy "{\"address\":\"$D\"}")
echo "$R" | grep -q '商品已售出' && ok "防重複購買：第二買家 D 被拒（已售）" || bad "防重複購買: $R"
MKT2=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/community/market")
echo "$MKT2" | grep -q '"sold":true' && ok "市集列表標記 sold=true" || bad "sold 標記: $(echo "$MKT2" | head -c 120)"
# 5. 廣告：B 發廣告 → 付款建議（pay_to＝獎勵池）。
R=$(J /api/community/ads/create "{\"address\":\"$B\",\"title\":\"TAC 算力租賃\",\"body\":\"穩定出塊\",\"budget_tacm\":3,\"target\":\"miners\"}")
echo "$R" | grep -q '"ad_id":1' && echo "$R" | grep -q '"pay_to":"reward_pool"' && echo "$R" | grep -q '"amount_tacm":3' && ok "廣告發佈：ad_id=1、付款建議 pay_to=reward_pool、3 TACM" || bad "廣告: $R"
# 5b. 廣告付款閉環：B 付款 → active、池 +3；非廣告主付款被拒。
PA0=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/pool" | $PY -c "import sys,json;d=json.load(sys.stdin);a=d.get('assets',[]);print([x.get('balance','0') for x in a if x.get('asset')=='TACm'][0] if a else '0')" 2>/dev/null)
R=$(J /api/community/ads/1/pay "{\"address\":\"$B\"}")
echo "$R" | grep -q '"status":"active"' && echo "$R" | grep -q '"amount_tacm":3' && ok "B 廣告一鍵付款（3 TACM 入池、投放中）" || bad "廣告付款: $R"
R=$(J /api/community/ads/1/pay "{\"address\":\"$C\"}")
echo "$R" | grep -q '僅廣告主可付款' && ok "非廣告主付款被拒（403）" || bad "非廣告主付款: $R"
ADS2=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/community/ads")
echo "$ADS2" | grep -q '"status":"active"' && ok "廣告池狀態=active（投放中）" || bad "ads 狀態: $(echo "$ADS2" | head -c 120)"
PA1=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/pool" | $PY -c "import sys,json;d=json.load(sys.stdin);a=d.get('assets',[]);print([x.get('balance','0') for x in a if x.get('asset')=='TACm'][0] if a else '0')" 2>/dev/null)
$PY -c "import sys;assert (float('$PA1')-float('$PA0'))/1e18 >= 2.99, '$PA0 -> $PA1';print(1)" 2>/dev/null && ok "資金池 +3 TACM（$PA0 → $PA1）" || bad "池未增: $PA0 -> $PA1"
# 6. stats：posts=3 comments=1 likes=1 ads=1 members=2。
ST=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/community/stats")
echo "$ST" | grep -q '"posts":3' && echo "$ST" | grep -q '"comments":1' && echo "$ST" | grep -q '"likes":1' && echo "$ST" | grep -q '"ads":1' && echo "$ST" | grep -q '"members":2' && ok "統計：3 貼文/1 留言/1 讚/1 廣告/2 成員" || bad "stats: $ST"
# 7. 快捷入金：B 轉 2 TACM 至交易所 → exchange 餘額增加。
R=$(J /api/community/transfer/exchange "{\"address\":\"$B\",\"amount\":\"2\"}")
echo "$R" | grep -q '"ok":true' && ok "社群快捷入金成功（鏈上費 $(echo "$R" | $PY -c "import sys,json;print(json.load(sys.stdin).get('chain_fee',''))" 2>/dev/null)）" || bad "入金: $R"
BAL=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/balances?uid=$B" | $PY -c "import sys,json;d=json.load(sys.stdin);print([b.get('avail','0') for b in d.get('balances',[]) if b.get('asset')=='TACm'][0] if d.get('balances') else '0')" 2>/dev/null)
OK=$($PY -c "import sys;assert float('$BAL')>=1.9, '$BAL';print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "交易所餘額 ≥1.9 TACM（實際 $BAL）" || bad "交易所餘額: $BAL"

# 8. /community 頁渲染。
CM=$(curl -s --max-time 5 "http://127.0.0.1:$WEB/community")
echo "$CM" | grep -q 'TAC 社群' && ok "頁面含『TAC 社群』" || bad "/community 渲染"
echo "$CM" | grep -q '動態牆' && ok "頁面含動態牆" || bad "動態牆"
echo "$CM" | grep -q 'bottom-nav' && ok "行動端底部導航已注入" || bad "底部導航"
echo "$CM" | grep -q '社群' && ok "頂欄導航含社群" || bad "頂欄導航缺社群"

echo ""
echo "================ 結果：PASS=$PASS FAIL=$FAIL ================"
[ "$FAIL" = "0" ]
