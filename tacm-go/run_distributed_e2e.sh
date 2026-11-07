#!/usr/bin/env bash
# 分佈式 BFT 多節點自動驗證：4 節點跨進程出塊/投票/最終性一致。
set -u
cd "$(dirname "$0")"
export GOTOOLCHAIN=local
N=4; BASE=8610; PASS=0; FAIL=0
log(){ echo "[dist] $*"; }
ok(){ PASS=$((PASS+1)); log "PASS: $1"; }
bad(){ FAIL=$((FAIL+1)); log "FAIL: $1"; }
pkill -x tacnode 2>/dev/null || true; sleep 0.5
go build -o ./tacnode ./cmd/node || exit 2
rm -rf ./distdata; mkdir -p ./distdata
SPEC=""
for i in $(seq 0 $((N-1))); do
  L=$(./tacnode -node-id "v$i" -data-dir "./distdata/v$i" -init-identity)
  [ -z "$SPEC" ] && SPEC="$L" || SPEC="$SPEC,$L"
done
PIDS=()
for i in $(seq 0 $((N-1))); do
  P=$((BASE+i)); PEERS=""; [ "$i" -gt 0 ] && PEERS="-peers http://127.0.0.1:$BASE"
  ./tacnode -node-id "v$i" -data-dir "./distdata/v$i" -port "$P" -block-time 1 -difficulty 1 -distributed -genesis-validators "$SPEC" $PEERS >"./distdata/v$i.log" 2>&1 &
  PIDS+=($!)
done
trap 'kill "${PIDS[@]}" 2>/dev/null; pkill -x tacnode 2>/dev/null' EXIT
sleep 5
H0=$(curl -sf "http://127.0.0.1:$BASE/status" | python3 -c 'import sys,json;print(json.load(sys.stdin)["block_height"])')
[ "${H0:-0}" -ge 3 ] && ok "4 節點共同出塊 height=$H0" || bad "出塊: ${H0:-0}"
for i in $(seq 1 $((N-1))); do
  H=$(curl -sf "http://127.0.0.1:$((BASE+i))/status" | python3 -c 'import sys,json;print(json.load(sys.stdin)["block_height"])')
  [ "$H" = "$H0" ] && ok "v$i 高度一致 $H" || bad "v$i 高度 $H vs $H0"
done
P=$(curl -sf "http://127.0.0.1:$BASE/p2p/peers" | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("peers",[])))')
[ "${P:-0}" -ge 3 ] && ok "P2P 互連 peers=$P" || bad "peers: $P"
FP=$(curl -sf "http://127.0.0.1:$BASE/finality-proof/3" | python3 -c 'import sys,json;d=json.load(sys.stdin);print("ok" if len(d.get("votes",[]))>=3 else "no")')
[ "$FP" = "ok" ] && ok "最終性證明 ≥3 precommit votes" || bad "finality: $FP"
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
