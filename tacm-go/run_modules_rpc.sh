#!/usr/bin/env bash
# 全模組 RPC 實機檢查：合約VM/L2/橋/SPV/錢包/獎勵池/聚合 一次跑通。
set -u
cd "$(dirname "$0")"
export GOTOOLCHAIN=local
BASE=8781; WEB=8782; PASS=0; FAIL=0
log(){ echo "[mod] $*"; }
ok(){ PASS=$((PASS+1)); log "PASS: $1"; }
bad(){ FAIL=$((FAIL+1)); log "FAIL: $1"; }
pkill -x tacnode 2>/dev/null || true; pkill -x tacweb 2>/dev/null || true; sleep 0.5
rm -rf .smoke_mod; mkdir -p .smoke_mod
go build -o ./tacweb ./cmd/web
nohup ./tacweb -data-dir ./.smoke_mod -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 -l2 -bridge >.smoke_mod/web.log 2>&1 &
WP=$!; trap 'kill $WP 2>/dev/null; pkill -x tacweb; pkill -x tacnode' EXIT
for i in $(seq 1 60); do curl -sf --max-time 5 --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done
J(){ curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
# 鏈核心
curl -sf --max-time 5 "http://127.0.0.1:$BASE/status" | grep -q block_height && ok "鏈 /status" || bad "status"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/mempool" >/dev/null && ok "鏈 /mempool" || bad "mempool"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/validators" >/dev/null && ok "鏈 /validators" || bad "validators"
# 合約 VM
A=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/wallet/new" | python3 -c "import sys,json;print(json.load(sys.stdin).get('address',''))" 2>/dev/null)
R=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/contract/preview?creator=$A&nonce=0")
echo "$R" | grep -q contract_address && ok "VM 部署地址預算" || bad "deploy: $R"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/contract/list" >/dev/null && ok "合約 /contract/list" || bad "clist"
# L2
R=$(J /l2/deposit '{"address":"tx0L2TST","amount":1000,"l1_tx_hash":"l1lock-e2e"}'); echo "$R" | grep -q ok && ok "L2 存款" || bad "l2dep: $R"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/l2/status" >/dev/null && ok "L2 /l2/status" || bad "l2st"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/l2/account/tx0L2TST" >/dev/null && ok "L2 帳戶" || bad "l2acc"
# 跨鏈橋
R=$(J /bridge/lock '{"source_chain":"tacm","target_chain":"ethereum","source_address":"tx0BR1","target_address":"0xBR2","amount":0.5,"token":"TACM","source_tx_hash":"l1lock-br"}'); echo "$R" | grep -q ok && ok "橋 lock 提案" || bad "lock: $R"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/bridge/chains" | grep -q '"tacm"' && ok "橋 /bridge/chains" || bad "chains"
curl -sf --max-time 5 "http://127.0.0.1:$BASE/bridge/stats" >/dev/null && ok "橋 /bridge/stats" || bad "bstats"
# 錢包+TiUSD（A 已於 VM 段取得）
[ -n "$A" ] && ok "錢包 /wallet/new" || bad "new"
J /api/wallet/deposit "{\"to\":\"$A\",\"asset\":\"TACm\",\"amount\":\"5\"}" >/dev/null
curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/wallet/info?address=$A" | grep -q '"tacm"' && ok "錢包餘額" || bad "winfo"
J /api/tiusd/mint "{\"to\":\"$A\",\"amount\":\"50\"}" >/dev/null
curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/tiusd/summary" | grep -q supply && ok "TiUSD summary" || bad "tiusd"
# 獎勵池
curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/rewardpool" | grep -q '"tacm"' && ok "獎勵池" || bad "pool"
# 聚合（等待至少 1 塊）
for i in $(seq 1 30); do H=$(curl -sf --max-time 2 "http://127.0.0.1:$BASE/status" | python3 -c "import sys,json;print(json.load(sys.stdin)['block_height'])" 2>/dev/null); [ "${H:-0}" -ge 1 ] && break; sleep 0.4; done
curl -sf --max-time 5 "http://127.0.0.1:$BASE/api/status" | python3 -c "
import sys,json; d=json.load(sys.stdin)
c={'ok':d['ok'],'h':d['chain']['block_height']>0,'wa':bool(d['wallet']['address']),'supply':'supply' in d['tiusd'],'rp':d['rewardpool']['tacm'] is not None,'fee':d['exchange']['fee_account'] is not None}
assert all(c.values()), str(c)
print('OK')" | grep -q OK && ok "聚合 /api/status" || bad "agg"
# Web 全頁
for u in / /wallet /exchange /dashboard /block/0 /static/sw.js; do
  C=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$WEB$u")
  [ "$C" = "200" ] && ok "頁面 $u 200" || bad "頁面 $u: $C"
done
PROP=$(curl -sf --max-time 5 "http://127.0.0.1:$BASE/validators" | python3 -c "import sys,json;v=json.load(sys.stdin).get('validators',[]);print(v[0]['address'] if v else '')")
curl -sf --max-time 5 "http://127.0.0.1:$WEB/address/$PROP" | grep -q "tx0" && ok "地址頁渲染" || bad "addrpage"
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
