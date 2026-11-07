#!/usr/bin/env bash
# 一鍵啟動 TAC 分佈式 BFT 測試網：默認 4 驗證人跨節點共同出塊、投票、
# 聚合 >2/3 precommit 形成跨進程最終性，proposer 按高度輪換。
# 用法：bash run_distributed.sh [節點數，默認 4]
# 需求：Go 1.23+。停止：Ctrl+C。
set -e
cd "$(dirname "$0")"
export GOTOOLCHAIN=local

N="${1:-4}"
BASE=8600

echo "[build] 編譯節點..."
go build -o ./tacnode ./cmd/node
rm -rf ./distdata
mkdir -p ./distdata

echo "[init] 生成 $N 個驗證人身份..."
SPEC=""
for i in $(seq 0 $((N - 1))); do
	LINE=$(./tacnode -node-id "v$i" -data-dir "./distdata/v$i" -init-identity)
	if [ -z "$SPEC" ]; then
		SPEC="$LINE"
	else
		SPEC="$SPEC,$LINE"
	fi
done

echo "[run] 啟動 $N 節點分佈式 BFT..."
PIDS=()
for i in $(seq 0 $((N - 1))); do
	PORT=$((BASE + i))
	PEERS=""
	[ "$i" -gt 0 ] && PEERS="-peers http://127.0.0.1:$BASE"
	./tacnode -node-id "v$i" -data-dir "./distdata/v$i" -port "$PORT" \
		-block-time 1 -difficulty 1 -distributed \
		-genesis-validators "$SPEC" $PEERS >"./distdata/v$i.log" 2>&1 &
	PIDS+=($!)
done

trap 'kill "${PIDS[@]}" 2>/dev/null' INT TERM

sleep 2
echo ""
echo "================ TAC 分佈式 BFT 測試網已啟動 ================"
for i in $(seq 0 $((N - 1))); do
	echo "  v$i 狀態：http://127.0.0.1:$((BASE + i))/status"
done
echo "  區塊列表：http://127.0.0.1:$BASE/blocks"
echo "  在線節點：http://127.0.0.1:$BASE/p2p/peers"
echo "  最終性證明：http://127.0.0.1:$BASE/finality-proof/2"
echo "  Web 界面（另開）：go run ./cmd/web -node-url http://127.0.0.1:$BASE"
echo "  停止：本視窗按 Ctrl+C"
echo "==========================================================="
echo ""

wait
