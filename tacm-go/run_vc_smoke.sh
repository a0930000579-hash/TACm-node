#!/usr/bin/env bash
# TAC M12 view-change 多數認證冒煙：4 節點分佈式 BFT，
# 殺掉當前輪值 proposer → 剩餘 3 節點（3/4 ≥ quorum=3）簽名認證 → round+1 接棒續推。
# 對照組：再殺 2 節點（剩 1/4 少數）→ 輪次不被劫持（鏈停屬正確行為）。
set -e
cd "$(dirname "$0")"
export GOTOOLCHAIN=local

BASE=8770
N=4

echo "[build] 編譯節點..."
go build -o ./tacnode ./cmd/node
rm -rf ./vcdata
mkdir -p ./vcdata

echo "[init] 生成 $N 節點身份與共享驗證人集..."
SPEC=""
for i in $(seq 0 $((N - 1))); do
	./tacnode -node-id "c$i" -data-dir "./vcdata/c$i" -init-identity >"./vcdata/c$i.id"
	LINE="c$i:$(cut -d: -f2,3 ./vcdata/c$i.id):1"
	[ -z "$SPEC" ] && SPEC="$LINE" || SPEC="$SPEC,$LINE"
done

echo "[run] 啟動 4 節點分佈式 BFT..."
PIDS=()
for i in $(seq 0 $((N - 1))); do
	PORT=$((BASE + i))
	PEERS=""
	[ "$i" -gt 0 ] && PEERS="-peers http://127.0.0.1:$BASE"
	./tacnode -node-id "c$i" -data-dir "./vcdata/c$i" -port "$PORT" \
		-block-time 1 -difficulty 1 -distributed -genesis-validators "$SPEC" $PEERS \
		>"./vcdata/c$i.log" 2>&1 &
	PIDS+=($!)
done
trap 'kill "${PIDS[@]}" 2>/dev/null' INT TERM

echo "[wait] 等待正常出塊至 finalized ≥ 3..."
for i in $(seq 1 25); do
	F=$(curl -sf http://127.0.0.1:$BASE/status | grep -o '"final_block_height":[0-9]*' | cut -d: -f2 || true)
	[ "${F:-0}" -ge 3 ] && break
	sleep 1
done
echo "  c0 finalized=$F"

echo ""
echo "===== 測試 1：殺掉 h4 輪值 proposer（(4)%4=0 → c0），剩餘 3/4 多數應認證接管 ====="
kill "${PIDS[0]}" 2>/dev/null
wait "${PIDS[0]}" 2>/dev/null || true

for i in $(seq 1 35); do
	F1=$(curl -sf http://127.0.0.1:$((BASE+1))/status | grep -o '"final_block_height":[0-9]*' | cut -d: -f2 || true)
	F3=$(curl -sf http://127.0.0.1:$((BASE+3))/status | grep -o '"final_block_height":[0-9]*' | cut -d: -f2 || true)
	if [ "${F1:-0}" -ge 6 ] && [ "${F3:-0}" -ge 6 ]; then
		break
	fi
	sleep 1
done
echo "  c1 finalized=$F1 | c3 finalized=$F3（期望 ≥6，view change 後續推）"
H3=$(curl -sf http://127.0.0.1:$((BASE+1))/blocks/4 2>/dev/null | grep -o '"hash":"[0-9a-f]*"' | cut -d'"' -f4)
H32=$(curl -sf http://127.0.0.1:$((BASE+3))/blocks/4 2>/dev/null | grep -o '"hash":"[0-9a-f]*"' | cut -d'"' -f4)
echo "  h4 哈希：c1=$H3 | c3=$H32（應一致）"
[ "$F1" -lt 6 ] && echo "[FAIL] view change 接管未發生" && exit 1
[ "$H3" != "$H32" ] && echo "[FAIL] view change 後分叉" && exit 1
echo "[PASS] 3/4 多數認證接管成功，鏈續推且無分叉"

echo ""
echo "===== 測試 2（對照）：再殺 c1/c2（剩 c3 單節點 1/4 少數），輪次不得被劫持 ====="
kill "${PIDS[1]}" "${PIDS[2]}" 2>/dev/null
sleep 4
F3=$(curl -sf http://127.0.0.1:$((BASE+3))/status | grep -o '"final_block_height":[0-9]*' | cut -d: -f2 || true)
echo "  c3 finalized=$F3（少數在線：不應強推，停滯屬正確）"
echo "[done] 冒煙完成。清理進程..."
kill "${PIDS[@]}" 2>/dev/null
wait "${PIDS[@]}" 2>/dev/null || true
echo "[done] 已停止。"
