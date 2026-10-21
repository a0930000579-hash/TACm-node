#!/usr/bin/env bash
# 一鍵啟動 TAC 自主智能鏈三節點本地測試網（A 出塊，B/C 跟隨）。
# 用法：bash run_network.sh
# 需求：已安裝 Go 1.23+。停止：Ctrl+C。
set -e
cd "$(dirname "$0")"
export GOTOOLCHAIN=local

echo "[build] 編譯節點..."
go build -o ./tacnode ./cmd/node
rm -rf ./netdata

echo "[run] 啟動出塊節點 A（:8501）..."
./tacnode -node-id A -port 8501 -data-dir ./netdata/A -block-time 1 -difficulty 1 &
sleep 1

echo "[run] 啟動跟隨節點 B（:8502）、C（:8503）..."
./tacnode -node-id B -port 8502 -data-dir ./netdata/B -block-time 1 \
  -difficulty 1 -peers http://127.0.0.1:8501 -follower &
./tacnode -node-id C -port 8503 -data-dir ./netdata/C -block-time 1 \
  -difficulty 1 -peers http://127.0.0.1:8501 -follower &

cat <<EOF

================ TAC 本地測試網已啟動 ================
RPC 狀態：  http://127.0.0.1:8501/status
節點 B：    http://127.0.0.1:8502/status
節點 C：    http://127.0.0.1:8503/status
在線節點：  http://127.0.0.1:8501/p2p/peers
最新區塊：  http://127.0.0.1:8501/blocks
停止：在本視窗按 Ctrl+C
======================================================

EOF
wait
