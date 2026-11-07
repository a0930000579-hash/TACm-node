#!/usr/bin/env bash
# TAC M11 跨鏈守衛網絡冒煙：3 節點 P2P + 守衛多簽跨鏈（Lock&Mint / Burn&Unlock）
# 驗證：分佈式簽名聚合 → 提案方執行 → 全網 exec 收斂 → 三節點狀態/哈希一致。
# 用法：bash run_bridge_smoke.sh
set -e
cd "$(dirname "$0")"
export GOTOOLCHAIN=local

BASE=8710
N=3

echo "[build] 編譯節點..."
go build -o ./tacnode ./cmd/node
rm -rf ./bridgedata
mkdir -p ./bridgedata

echo "[init] 生成 $N 節點身份..."
IDS=()
for i in $(seq 0 $((N - 1))); do
	./tacnode -node-id "b$i" -data-dir "./bridgedata/b$i" -init-identity \
		>"./bridgedata/b$i.identity"
	IDS+=("$(cat ./bridgedata/b$i.identity)")
done

# 守衛規格：addr:pubkey（無 power），前 2 節點為守衛、門檻 required=2。
G1="$(echo "${IDS[0]}" | cut -d: -f2,3)"
G2="$(echo "${IDS[1]}" | cut -d: -f2,3)"
GUARDIANS="$G1,$G2"

echo "[run] 啟動 3 節點（含跨鏈守衛網絡）..."
PIDS=()
for i in $(seq 0 $((N - 1))); do
	PORT=$((BASE + i))
	PEERS=""
	[ "$i" -gt 0 ] && PEERS="-peers http://127.0.0.1:$BASE"
	./tacnode -node-id "b$i" -data-dir "./bridgedata/b$i" -port "$PORT" \
		-block-time 1 -difficulty 1 -distributed \
		-genesis-validators "${IDS[0]},${IDS[1]},${IDS[2]}" \
		-bridge -bridge-guardians "$GUARDIANS" $PEERS \
		>"./bridgedata/b$i.log" 2>&1 &
	PIDS+=($!)
done
trap 'kill "${PIDS[@]}" 2>/dev/null' INT TERM

echo "[wait] 等待鏈出塊與 P2P 互聯..."
for i in $(seq 1 20); do
	H=$(curl -sf "http://127.0.0.1:$BASE/status" | grep -o '"height":[0-9]*' | head -1 || true)
	P=$(curl -sf "http://127.0.0.1:$((BASE+1))/p2p/peers" | grep -o '"count":[0-9]*' | head -1 || true)
	if [ "$H" = '"height":2' ] && [ "$P" = '"count":2' ]; then
		break
	fi
	sleep 1
done
echo "  b0 高度: $H | b1 peers: $P"

echo ""
echo "===== 測試 1：Lock&Mint（b0 發起，鎖定 TACM → 以太坊，守衛多簽批准）====="
LOCK=$(curl -sf -X POST http://127.0.0.1:$BASE/bridge/lock \
	-H 'Content-Type: application/json' \
	-d '{"source_chain":"tacm","target_chain":"ethereum","source_address":"tx0-lock-alice","target_address":"0xTargetAlice","amount":10,"token":"TACM","source_tx_hash":"0xsmoke-lock-1"}')
echo "  響應: $LOCK"
BID=$(echo "$LOCK" | grep -o '"bridge_tx_id":"[^"]*"' | cut -d'"' -f4)

for i in $(seq 1 20); do
	S=$(curl -sf -X POST http://127.0.0.1:$BASE/bridge/status -H 'Content-Type: application/json' \
		-d "{\"bridge_tx_id\":\"$BID\"}" | grep -o '"status":"[^"]*"' | cut -d'"' -f4 || true)
	[ "$S" = "confirmed" ] && break
	sleep 1
done
echo "  b0 跨鏈狀態: $S（期望 confirmed）"
curl -sf -X POST http://127.0.0.1:$BASE/bridge/status -H 'Content-Type: application/json' \
	-d "{\"bridge_tx_id\":\"$BID\"}"

echo ""
echo "===== 測試 2：Burn&Unlock（b1 發起，銷毀 BSC 側 → 解鎖回 tacm）====="
BURN=$(curl -sf -X POST http://127.0.0.1:$((BASE+1))/bridge/burn \
	-H 'Content-Type: application/json' \
	-d '{"source_chain":"bsc","target_chain":"tacm","source_address":"0xSourceBob","target_address":"tx0-burn-bob","amount":4,"token":"TACM","source_tx_hash":"0xsmoke-burn-2"}')
echo "  響應: $BURN"
BID2=$(echo "$BURN" | grep -o '"bridge_tx_id":"[^"]*"' | cut -d'"' -f4)
for i in $(seq 1 20); do
	S2=$(curl -sf -X POST http://127.0.0.1:$((BASE+1))/bridge/status -H 'Content-Type: application/json' \
		-d "{\"bridge_tx_id\":\"$BID2\"}" | grep -o '"status":"[^"]*"' | cut -d'"' -f4 || true)
	[ "$S2" = "confirmed" ] && break
	sleep 1
done
echo "  b1 反向跨鏈狀態: $S2（期望 confirmed）"

echo ""
echo "===== 測試 3：全網收斂一致性 ====="
for i in $(seq 0 $((N - 1))); do
	R=$(curl -sf -X POST http://127.0.0.1:$((BASE+i))/bridge/status -H 'Content-Type: application/json' \
		-d "{\"bridge_tx_id\":\"$BID\"}" | grep -o '"target_tx_hash":"[^"]*"' | cut -d'"' -f4)
	echo "  b$i 鎖定交易的 target hash: $R"
done
for i in $(seq 0 $((N - 1))); do
	R2=$(curl -sf -X POST http://127.0.0.1:$((BASE+i))/bridge/status -H 'Content-Type: application/json' \
		-d "{\"bridge_tx_id\":\"$BID2\"}" | grep -o '"target_tx_hash":"[^"]*"' | cut -d'"' -f4)
	echo "  b$i 銷毀交易的 target hash: $R2"
done

echo ""
echo "===== 測試 4：守衛簽名持久化（可審計） ====="
curl -sf -X POST http://127.0.0.1:$BASE/bridge/status -H 'Content-Type: application/json' \
	-d "{\"bridge_tx_id\":\"$BID\"}" | grep -o '"signatures":\[[^]]*\]'

echo ""
echo "[done] 冒煙完成。清理進程..."
kill "${PIDS[@]}" 2>/dev/null
wait "${PIDS[@]}" 2>/dev/null || true
echo "[done] 已停止。"
