#!/usr/bin/env bash
# TAC 自主智能鏈 M20-2 挖礦中心＋社群實機驗證：
# 真進程 tacweb（-l2 -bridge + 社群 env）→ 出塊 → 驗證
#   1) /mining 頁渲染與其 4 個 RPC 資料源
#   2) coinbase 分潤真實入帳：節點(礦工) ≥ 88%×10×h、獎勵池 ≥ 12%×10×h（wei 整數下界）
#   3) 社群配置 → 聚合 /api/status.community → 頂欄膠囊渲染（可點外鏈）
set -u
BASE=8781; WEB=8782; DATA=".smoke_mining"
ROOT="$(cd "$(dirname "$0")" && pwd)"; cd "$ROOT"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "[mine] PASS: $*"; }
bad() { FAIL=$((FAIL+1)); echo "[mine] FAIL: $*"; }
log() { echo "[mine] $*"; }

# 1. 準備（重建 binary，清資料，社群 env 啟用）。
pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$DATA" && mkdir -p "$DATA"
( cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web ) || { echo "build failed"; exit 2; }

TAC_COMMUNITY_TELEGRAM=https://t.me/tacm_test \
TAC_COMMUNITY_X=https://x.com/tacm_test \
nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 -l2 -bridge >"$DATA/w.log" 2>&1 &
trap 'pkill -x tacweb 2>/dev/null' EXIT

# 2. 等待就緒並出塊（health → 至少 3 塊）。
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done
sleep 0.5
H=""
for i in $(seq 1 30); do
  H=$(curl -sf --max-time 2 "http://127.0.0.1:$BASE/status" | /opt/python3.12/bin/python3 -c "import sys,json;print(json.load(sys.stdin).get('block_height',0))" 2>/dev/null)
  [ "${H:-0}" -ge 3 ] && break
  sleep 0.5
done
[ "${H:-0}" -ge 3 ] && ok "出塊 ≥3（實際 $H）" || bad "出塊不足: h=$H"

# 3. 挖礦中心頁 200 + 內容。
M=$(curl -s --max-time 5 "http://127.0.0.1:$WEB/mining")
echo "$M" | grep -q '我的礦機' && ok "頁面 /mining 含『我的礦機』（App 化礦機視角）" || bad "/mining 內容"
echo "$M" | grep -q '在線礦工排行' && ok "頁面含在線礦工排行卡" || bad "在線礦工排行卡"
echo "$M" | grep -q 'href="/mining"' && ok "頂欄導航含挖礦" || bad "導航缺挖礦"

# 4. /mining 頁 4 個 RPC 資料源全部 200（頁面 JS 即時讀取）。
for ep in /status /api/rewardpool /validators /api/status; do
  curl -sf --max-time 5 "http://127.0.0.1:$BASE$ep" >/dev/null 2>&1 && ok "挖礦資料源 $ep 200" || bad "資料源 $ep 失敗"
done

# 5. coinbase 分潤真實入帳（wei 整數下界：提議者 ≥ 8.5e18×h；獎勵池 ≥ 1.5e18×h）。
NODE_ADDR=$(curl -sf "http://127.0.0.1:$BASE/status" | /opt/python3.12/bin/python3 -c "import sys,json;print(json.load(sys.stdin)['address'])")
W=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$NODE_ADDR")
NODE_TACM=$(echo "$W" | /opt/python3.12/bin/python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('tacm_raw') or d.get('tacm','0'))" 2>/dev/null)
[ -z "$NODE_TACM" ] && NODE_TACM=0
RP=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/rewardpool" | /opt/python3.12/bin/python3 -c "import sys,json;print(json.load(sys.stdin).get('tacm','0'))")
MIN_NODE=$((88 * 10 * H * 1000000000000000000 / 100))
MIN_POOL=$((12 * 10 * H * 1000000000000000000 / 100))
CMP=$(/opt/python3.12/bin/python3 -c "import sys; print('ok' if int('${NODE_TACM:-0}') >= $MIN_NODE else 'no')")
[ "$CMP" = "ok" ] && ok "節點礦工 coinbase 88% 入帳（h=$H 得 ${NODE_TACM} ≥ $MIN_NODE wei）" || bad "節點礦工分潤不足: ${NODE_TACM} < $MIN_NODE"
CMP=$(/opt/python3.12/bin/python3 -c "import sys; print('ok' if int('${RP:-0}') >= $MIN_POOL else 'no')")
[ "$CMP" = "ok" ] && ok "獎勵池 12% 挹注（h=$H 池 ${RP} ≥ $MIN_POOL wei）" || bad "獎勵池挹注不足: $RP < $MIN_POOL"

# 6. 社群：聚合欄位 → 頁面膠囊（可點外鏈、target=_blank）。
COM=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/status" | /opt/python3.12/bin/python3 -c "import sys,json;d=json.load(sys.stdin)['community'];print(d.get('telegram',''))")
[ "$COM" = "https://t.me/tacm_test" ] && ok "聚合 community.telegram 生效" || bad "community 未生效: $COM"
echo "$M" | grep -q 'href="https://t.me/tacm_test"' && ok "頂欄 Telegram 膠囊渲染（可點）" || bad "Telegram 膠囊未渲染"
echo "$M" | grep -q 'target="_blank"' && ok "社群連結新分頁開啟" || bad "外鏈未新分頁"

# M33：礦機伺服器排程——開機即綠（active+online）、關機即灰、無前端心跳仍持續。
SADDR=$NODE_ADDR
curl -sf --max-time 5 -X POST "http://127.0.0.1:$BASE/api/miner/start" -H 'Content-Type: application/json' -d "{\"address\":\"$SADDR\"}" >/dev/null
MS=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/miners")
echo "$MS" | grep -q '"active":true' && ok "開機後 active=true（伺服器排程）" || bad "開機 active 未生效"
echo "$MS" | grep -q '"online":true' && ok "開機後 online=true（訊號即時綠）" || bad "開機 online 未生效"
curl -sf --max-time 5 -X POST "http://127.0.0.1:$BASE/api/miner/stop" -H 'Content-Type: application/json' -d "{\"address\":\"$SADDR\"}" >/dev/null
MS2=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/miners")
echo "$MS2" | grep -q '"online":false' && ok "關機後即時離線（訊號灰）" || bad "關機未即時離線"
curl -sf --max-time 5 -X POST "http://127.0.0.1:$BASE/api/miner/start" -H 'Content-Type: application/json' -d "{\"address\":\"$SADDR\"}" >/dev/null
sleep 13
MS3=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/miners")
echo "$MS3" | grep -q '"online":true' && ok "離開頁面 13s 仍線上（伺服器持續心跳）" || bad "伺服器心跳未持續"

echo
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
