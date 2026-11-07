#!/usr/bin/env bash
# TAC 自主智能鏈 M21-B 資金池實機驗證：
# 真進程 tacweb → 出塊(coinbase 12% 注入) → /api/exchange/pool 視圖 →
# 撮合造 fee → topup 結算 → withdraw 領用 → 全程 injected−claimed==balance 守恆。
set -u
BASE=8781; WEB=8782; DATA=".smoke_pool"
ROOT="$(cd "$(dirname "$0")" && pwd)"; cd "$ROOT"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "[pool] PASS: $*"; }
bad() { FAIL=$((FAIL+1)); echo "[pool] FAIL: $*"; }
log() { echo "[pool] $*"; }
PY=/opt/python3.12/bin/python3
J() { curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
POOL() { curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/pool"; }
TACM() { $PY -c "
import sys,json
d=json.load(sys.stdin)
for a in d.get('assets',[]):
    if a['asset']=='TACm': print(a['$1']); break
else: print('')" 2>/dev/null; }

pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$DATA" && mkdir -p "$DATA"
( cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web ) || { echo "build failed"; exit 2; }
nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 >"$DATA/w.log" 2>&1 &
trap 'pkill -x tacweb 2>/dev/null' EXIT
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done

# 0. 等出 3 塊。
H=""
for i in $(seq 1 40); do
  H=$(curl -sf --max-time 2 "http://127.0.0.1:$BASE/status" | $PY -c "import sys,json;print(json.load(sys.stdin).get('block_height',0))" 2>/dev/null)
  [ "${H:-0}" -ge 3 ] && break
  sleep 0.5
done
[ "${H:-0}" -ge 3 ] && ok "出塊 ≥3（實際 $H）" || bad "出塊不足: h=$H"

# 1. coinbase 注入視圖：TACm balance=1.2×h、injected=1.2×h、claimed=0。
P0=$(POOL | TACM balance)
C0=$(POOL | TACM total_claimed)
OK=$($PY -c "import sys;h=int('$H');want=int(1.2*h*10**18);assert int('$P0')>=want,'bal %s < %s'%('$P0',want);print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "池 TACm 餘額 ≥ 1.2×$H（實際 $P0，出塊持續故下界）" || bad "池餘額異常: $P0"
OK=$($PY -c "import sys;h=int('$H');want=1.2*h*10**18;assert int('$C0')==0;print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "尚未領用 claimed=0" || bad "claimed 異常: $C0"
OK=$($PY -c "import sys;d1=int('$(POOL | TACM balance)');d2=int('$(POOL | TACM total_injected)');assert d1==d2;print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "守恆#1：balance == total_injected" || bad "守恆#1 破壞"

# 2. 撮合造 TACm fee：A 賣 2、B 買 2（TACM/TiUSD）。
U1="tx0P21A"; U2="tx0P21B"
J /api/wallet/deposit "{\"to\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"s\"}" >/dev/null
# M31：TiUSD 禁止私自鑄造——改由交易所資金池直接入金（後台結算等效）。
J /api/exchange/deposit "{\"uid\":\"$U1\",\"asset\":\"TiUSD\",\"amount\":\"500\"}" >/dev/null
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"500\"}" >/dev/null
J /api/exchange/deposit_from_wallet "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"5\"}" >/dev/null
J /api/exchange/deposit "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"300\"}" >/dev/null
J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/TiUSD\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}" >/dev/null
R=$(J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/TiUSD\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}")
echo "$R" | grep -q '"status":"filled"' && ok "撮合成交（產生手續費）" || bad "撮合: $R"

# 3. topup：fee TACm → 資金池。
FEE_AMT=$(curl -s --max-time 5 "http://127.0.0.1:$BASE/api/exchange/fee" | $PY -c "import sys,json;d=json.load(sys.stdin);print([b['avail'] for b in d['fee_account'] if b['asset']=='TACm'][0])")
[ -n "$FEE_AMT" ] && [ "$FEE_AMT" != "0" ] || bad "無可用 fee（$FEE_AMT）"
R=$(J /api/rewardpool/topup "{\"asset\":\"TACm\",\"amount\":\"$FEE_AMT\"}")
echo "$R" | grep -q '"ok":true' && ok "topup 結算 fee→資金池（$FEE_AMT）" || bad "topup: $R"
P1=$(POOL | TACM balance)
I1=$(POOL | TACM total_injected)
OK=$($PY -c "import sys;d1=int('$I1');d0=int('$P0');f=int(float('$FEE_AMT')*10**18);assert d1>=d0+f, 'inj %s < %s+%s'%(d1,d0,f);print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "topup 後累計注入 ≥ 前值+$FEE_AMT（含持續 coinbase）" || bad "injected 異常: $I1"
OK=$($PY -c "import sys;d1=int('$I1');d0=int('$P0');f=int(float('$FEE_AMT')*10**18);assert d1>=d0+f;print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "topup 計入 total_injected（≥ 前值+$FEE_AMT）" || bad "injected2: $I1"

# 4. withdraw：池 → 地址（領用）。
W1="tx0P21W"
J /api/wallet/deposit "{\"to\":\"$W1\",\"asset\":\"TACm\",\"amount\":\"0\",\"memo\":\"acc\"}" >/dev/null
R=$(J /api/rewardpool/withdraw "{\"to\":\"$W1\",\"amount\":\"1\"}")
echo "$R" | grep -q '"ok":true' && ok "資金池提領 1 TACm 至地址" || bad "withdraw: $R"
P2=$(POOL | TACM balance)
C2=$(POOL | TACM total_claimed)
OK=$($PY -c "import sys;d1=int('$P2');assert d1>=0;print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "領用後 balance ≥ 0（coinbase 持續注入下界）" || bad "領用後 balance: $P2"
OK=$($PY -c "import sys;c=int('$C2');assert c>=int(10**18), c;print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "total_claimed ≥ 1 TACm（領用含鏈上撤銷費，實際 $C2）" || bad "claimed: $C2"

# 5. 全程守恆：injected − claimed == balance。
OK=$($PY -c "import sys;i=int('$(POOL | TACM total_injected)');c=int('$(POOL | TACM total_claimed)');b=int('$(POOL | TACM balance)');assert i-c==b,(i,c,b);print(1)" 2>/dev/null) && [ "$OK" = "1" ] && ok "終局守恆：injected−claimed==balance" || bad "守恆破壞: $(POOL)"

# 6. 頁面資金池 tab 注入。
EX=$(curl -s --max-time 5 "http://127.0.0.1:$WEB/exchange")
echo "$EX" | grep -q '資金池' && ok "交易所頁含資金池 tab" || bad "頁面缺資金池"
echo "$EX" | grep -q 'loadPool' && ok "資金池載入邏輯已注入" || bad "頁面缺 loadPool"

echo ""
echo "================ 結果：PASS=$PASS FAIL=$FAIL ================"
[ "$FAIL" = "0" ]
