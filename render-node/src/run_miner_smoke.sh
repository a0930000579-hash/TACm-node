#!/usr/bin/env bash
# TAC 自主智能鏈 M21-A 礦機實機驗證：
# 真進程 tacweb → 節點自動註冊自身礦機(1vCPU) → 註冊 m2(2vCPU) → 出 3 塊 →
#   1) /api/miners：在線 2 台、總算力 3M
#   2) coinbase 原本方式：池 12%×10×h、剩餘 88% 按算力 1:2 瓜分（wei 下界斷言）
#   3) /api/miner/earnings 收益 API 與瓜分金額一致
#   4) /mining 頁 App 化標記（我的礦機／在線礦工排行）
set -u
BASE=8781; WEB=8782; DATA=".smoke_miner"
ROOT="$(cd "$(dirname "$0")" && pwd)"; cd "$ROOT"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "[miner] PASS: $*"; }
bad() { FAIL=$((FAIL+1)); echo "[miner] FAIL: $*"; }
log() { echo "[miner] $*"; }
PY=/opt/python3.12/bin/python3

pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$DATA" && mkdir -p "$DATA"
( cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web ) || { echo "build failed"; exit 2; }

nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 >"$DATA/w.log" 2>&1 &
trap 'pkill -x tacweb 2>/dev/null' EXIT

for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done
sleep 0.5

# 1. 節點地址（自動註冊為默認礦機）＋產生第二台礦機地址。
NODE_ADDR=$(curl -sf "http://127.0.0.1:$BASE/status" | $PY -c "import sys,json;print(json.load(sys.stdin)['address'])")
M2=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
[ -n "$M2" ] && ok "產生第二台礦機地址 ${M2:0:12}…" || bad "wallet/new 失敗"

# 2. 註冊 m2（2 vCPU）＋心跳；節點自身已在啟動時註冊（1 vCPU）。
R=$(curl -s -X POST "http://127.0.0.1:$BASE/api/miner/register" -H 'Content-Type: application/json' -d "{\"address\":\"$M2\",\"vcpu\":2,\"vgpu\":0}")
echo "$R" | grep -q '"ok":true' && ok "m2 註冊成功 (2vCPU)" || bad "m2 註冊: $R"
T=$(curl -s -X POST "http://127.0.0.1:$BASE/api/miner/tick" -H 'Content-Type: application/json' -d "{\"address\":\"$M2\"}")
echo "$T" | grep -q '"ok":true' && ok "m2 心跳成功" || bad "m2 心跳: $T"

# 3. 等出 3 塊。
H=""
for i in $(seq 1 40); do
  H=$(curl -sf --max-time 2 "http://127.0.0.1:$BASE/status" | $PY -c "import sys,json;print(json.load(sys.stdin).get('block_height',0))" 2>/dev/null)
  [ "${H:-0}" -ge 3 ] && break
  sleep 0.5
done
[ "${H:-0}" -ge 3 ] && ok "出塊 ≥3（實際 $H）" || bad "出塊不足: h=$H"

# 4. /api/miners：在線 2 台、總算力 3M。
MINERS=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/miners")
ON=$(echo "$MINERS" | $PY -c "import sys,json;d=json.load(sys.stdin);print(d.get('online_count',0))")
[ "$ON" = "2" ] && ok "在線礦工 = 2（節點+m2）" || bad "在線礦工數=$ON 應為 2（${MINERS:0:160}）"
SUMHR=$(echo "$MINERS" | $PY -c "
import sys,json
d=json.load(sys.stdin)
s=sum(int(x['share']) for x in d.get('splits',[]))
print(s)")
[ "$SUMHR" = "3000000" ] && ok "總算力 = 3,000,000（1M+2M）" || bad "總算力=$SUMHR 應為 3000000"

# 5. coinbase 瓜分斷言（原本方式）：池 12%、剩餘 88% 按算力 1:2。
P=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/rewardpool" | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm','0'))")
N=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$NODE_ADDR" | $PY -c "import sys,json;d=json.load(sys.stdin);print(d.get('tacm_raw') or d.get('tacm','0'))")
M=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$M2" | $PY -c "import sys,json;d=json.load(sys.stdin);print(d.get('tacm_raw') or d.get('tacm','0'))")
OKP=$($PY -c "import sys;h=int('$H');want=1.2*h;assert int('$P')>=int(want*0.98*1e18),'pool %s<%s'%('$P',want);print(1)" 2>/dev/null) && [ "$OKP" = "1" ] && ok "獎勵池挹注 ≥ 1.2×$H TACm（實際 $P wei）" || bad "獎勵池挹注異常: $P"
OKN=$($PY -c "import sys;h=int('$H');want=8.8*h/3;assert int('$N')>=int(want*0.98*1e18),'node %s<%s'%('$N',want);print(1)" 2>/dev/null) && [ "$OKN" = "1" ] && ok "節點分潤 ≈ 8.8×$H/3（算力1/3，實際 $N wei）" || bad "節點分潤異常: $N"
OKM=$($PY -c "import sys;h=int('$H');want=8.8*h*2/3;assert int('$M')>=int(want*0.98*1e18),'m2 %s<%s'%('$M',want);print(1)" 2>/dev/null) && [ "$OKM" = "1" ] && ok "m2 分潤 ≈ 8.8×$H×2/3（算力2/3，實際 $M wei）" || bad "m2 分潤異常: $M"
TOTAL=$($PY -c "print(int('$P')+int('$N')+int('$M'))")
[ "$TOTAL" = "$($PY -c "print(int(10*int('$H')*10**18))")" ] && ok "守恆：池+節點+m2 = 10×$H TACm 全額入帳" || bad "總額=$TOTAL 應=10e18×$H"

# 6. 收益 API 一致性。
EARN=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/miner/earnings?address=$M2" | $PY -c "import sys,json;print(json.load(sys.stdin).get('earned_raw','0'))")
$PY -c "import sys;e=int('$EARN');m=int('$M');assert e>=m and (e-m)<=6*10**18, (e,m);print('OK')" 2>/dev/null   && ok "/api/miner/earnings 累計收益 ≥ 錢包餘額（差 ≤1 輪：$EARN / $M wei）" || bad "earnings=$EARN 錢包=$M"

# 7. /mining 頁 App 化標記。
MINE=$(curl -s --max-time 5 "http://127.0.0.1:$WEB/mining")
echo "$MINE" | grep -q '我的礦機' && ok "頁面含『我的礦機』（App 化礦機視角）" || bad "/mining 缺我的礦機"
echo "$MINE" | grep -q '在線礦工排行' && ok "頁面含『在線礦工排行』" || bad "/mining 缺排行"
echo "$MINE" | grep -q 'bottom-nav' && ok "行動端底部導航已注入" || bad "/mining 缺底部導航"

echo ""
echo "================ 結果：PASS=$PASS FAIL=$FAIL ================"
[ "$FAIL" = "0" ]
