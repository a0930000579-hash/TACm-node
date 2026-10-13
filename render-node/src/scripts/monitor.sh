#!/usr/bin/env bash
# TAC 節點監控：健康/高度/供應/獎勵池/P2P peer
# 用法：./monitor.sh [RPC_URL]   （預設 http://127.0.0.1:8080）
set -euo pipefail

BASE="${1:-http://127.0.0.1:8080}"

echo "=== TAC 節點監控 $(date '+%F %T') ==="
echo "RPC: $BASE"

health=$(curl -sf --max-time 5 "$BASE/health" || echo '{"ok":false}')
echo "健康: $health"

status=$(curl -sf --max-time 5 "$BASE/status" || echo '{}')
h=$(echo "$status" | python3 -c "import json,sys;d=json.load(sys.stdin);print(d.get('block_height','?'), d.get('effective_difficulty','?'), d.get('consensus','?'))" 2>/dev/null || echo '解析失敗')
echo "高度/難度/共識: $h"

stats=$(curl -sf --max-time 5 "$BASE/api/chain/stats" || echo '{}')
echo "全鏈統計: $stats"

peers=$(curl -sf --max-time 5 "$BASE/p2p/peers" || echo '{"count":0}')
echo "P2P peers: $peers"
