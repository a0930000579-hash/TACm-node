#!/usr/bin/env bash
# ============================================================
# TAC 自主智能鏈 — 一鍵啟動 3 節點分佈式 BFT 演示網絡
# 用法:  ./tacnet.sh [start|stop|status]
#   start: 建置並啟動 node1(8332)+node2(8333)+node3(8334)
#   stop : 停止全部節點
#   status: 顯示各節點鏈頂與同步狀態
# 說明: 三個節點互為創世驗證人（輪值出塊＋投票），
#       證明 TAC 具備多節點去中心化共識，非單點假鏈。
# ============================================================
set -euo pipefail
cd "$(dirname "$0")"

BIN=./tacnode
NETDIR=./tacnet
P1=8332; P2=8333; P3=8334
SEED=http://127.0.0.1:$P1
BT=2; DIFF=2

build_bin() {
  if [ ! -x "$BIN" ]; then
    echo "[build] 編譯 tacnode..."
    GOTOOLCHAIN=local go build -o tacnode ./cmd/node
  fi
}

gen_identities() {
  local spec=""
  local i=1
  for id in node1 node2 node3; do
    echo "[identity] 生成 $id 身份..."
    local line
    line=$("$BIN" -node-id "$id" -init-identity -data-dir "$NETDIR/ident$i" 2>/dev/null | tail -1)
    spec="${spec}${line},"
    i=$((i+1))
  done
  VALS="${spec%,}"
  echo "[identity] 創世驗證人: $VALS"
}

start() {
  mkdir -p "$NETDIR"
  gen_identities
  # 節點直接沿用各自身份目錄（ident1/2/3），確保驗證人身份與創世規格一致。
  echo "[start] 啟動 node1 (port $P1)..."
  ( "$BIN" -node-id node1 -port $P1 -data-dir "$NETDIR/ident1" -block-time $BT -difficulty $DIFF -genesis-validators "$VALS" -distributed >"$NETDIR/node1.log" 2>&1 & )
  sleep 1
  echo "[start] 啟動 node2 (port $P2, seed $SEED)..."
  ( "$BIN" -node-id node2 -port $P2 -data-dir "$NETDIR/ident2" -peers "$SEED" -block-time $BT -difficulty $DIFF -genesis-validators "$VALS" -distributed >"$NETDIR/node2.log" 2>&1 & )
  sleep 1
  echo "[start] 啟動 node3 (port $P3, seed $SEED)..."
  ( "$BIN" -node-id node3 -port $P3 -data-dir "$NETDIR/ident3" -peers "$SEED" -block-time $BT -difficulty $DIFF -genesis-validators "$VALS" -distributed >"$NETDIR/node3.log" 2>&1 & )
  echo "[start] 已啟動 3 節點，等待出塊..."
  sleep 4
  status
}

stop() {
  echo "[stop] 停止全部節點..."
  pkill -f 'tacnode -node-id node[123]' 2>/dev/null || true
  sleep 1
  echo "[stop] 完成"
}

status() {
  echo "=============================="
  echo " TAC 3 節點網路狀態"
  echo "=============================="
  for p in $P1 $P2 $P3; do
    if curl -s -m 3 "http://127.0.0.1:$p/api/status" >/dev/null 2>&1; then
      local h f c
      h=$(curl -s -m 3 "http://127.0.0.1:$p/api/status" | grep -oP '"block_height":\K[0-9]+' || echo "?")
      f=$(curl -s -m 3 "http://127.0.0.1:$p/api/status" | grep -oP '"final_block_height":\K[0-9]+' || echo "?")
      c=$(curl -s -m 3 "http://127.0.0.1:$p/api/status" | grep -oP '"consensus":\K"[^"]+"' || echo "?")
      echo "  port $p : 高度 $h / 最終化 $f / 共識 $c  ✅"
    else
      echo "  port $p : 離線 ❌"
    fi
  done
}

case "${1:-start}" in
  start) build_bin; start ;;
  stop)  stop ;;
  status) status ;;
  *) echo "用法: $0 [start|stop|status]"; exit 1 ;;
esac
