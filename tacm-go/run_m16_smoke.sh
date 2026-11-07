#!/usr/bin/env bash
# TAC 自主智能鏈 M16 冒煙：獎勵池機制。
# 出塊 coinbase 15% 挹注 → 交易所 fee 結算入池 → 池查詢/提取 → 守恆。
set -u
BASE=8760
ROOT="$(cd "$(dirname "$0")" && pwd)"
DATA="$ROOT/.smoke_m16"
PASS=0; FAIL=0
log()  { echo "[smoke] $*"; }
ok()   { PASS=$((PASS+1)); log "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); log "FAIL: $1"; }

pkill -x tacnode 2>/dev/null; sleep 0.4
rm -rf "$DATA"; mkdir -p "$DATA"
(cd "$ROOT" && GOTOOLCHAIN=local go build -o tacnode ./cmd/node) || { echo "build failed"; exit 2; }
# (build already produced binary)
nohup ./tacnode -data-dir "$DATA" -port $BASE -block-time 1 -difficulty 1 >"$DATA/node.log" 2>&1 &
NODE_PID=$!
trap 'kill $NODE_PID 2>/dev/null; pkill -x tacnode 2>/dev/null' EXIT
for i in $(seq 1 20); do curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.3; done
curl -sf "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && ok "節點啟動" || bad "節點啟動"

J() { curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
P() { curl -s "http://127.0.0.1:$BASE/api/rewardpool" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('tacm',''))"; }

# 0. 等出 3 個塊（coinbase 挹注 pool 3 次）。
sleep 3.5
P0=$(P)
[ "$P0" != "0" ] && [ -n "$P0" ] && ok "出塊後獎勵池已挹注（$P0 TACM）" || bad "獎勵池挹注: [$P0]"

# 1. 撮合產生交易所手續費：A 賣 2 TACM（TiUSD 報價）、B 買 2。
U1="tx0M16A"; U2="tx0M16B"
J /api/wallet/deposit "{\"to\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"10\",\"memo\":\"s\"}" >/dev/null
J /api/tiusd/mint "{\"to\":\"$U1\",\"amount\":\"500\",\"note\":\"s\"}" >/dev/null
J /api/tiusd/mint "{\"to\":\"$U2\",\"amount\":\"500\",\"note\":\"s\"}" >/dev/null
J /api/exchange/deposit_from_wallet "{\"uid\":\"$U1\",\"asset\":\"TACm\",\"amount\":\"5\"}" >/dev/null
J /api/exchange/deposit_from_wallet "{\"uid\":\"$U2\",\"asset\":\"TiUSD\",\"amount\":\"300\"}" >/dev/null
J /api/exchange/order "{\"uid\":\"$U1\",\"market\":\"TACM/TiUSD\",\"side\":\"sell\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}" >/dev/null
R=$(J /api/exchange/order "{\"uid\":\"$U2\",\"market\":\"TACM/TiUSD\",\"side\":\"buy\",\"type\":\"limit\",\"price\":\"100\",\"qty\":\"2\"}")
echo "$R" | grep -q '"status":"filled"' && ok "撮合成交（產生手續費）" || bad "撮合: $R"
R=$(curl -s "http://127.0.0.1:$BASE/api/exchange/fee")
echo "$R" | grep -q '"TACm"' && ok "交易所 fee 帳戶有 TACm" || bad "fee 帳戶: $R"

# 2. topup：交易所 fee TACm → 鏈上獎勵池。
FEE_AMT=$(curl -s "http://127.0.0.1:$BASE/api/exchange/fee" | python3 -c "import sys,json;d=json.load(sys.stdin);print([b['avail'] for b in d['fee_account'] if b['asset']=='TACm'][0])")
[ -n "$FEE_AMT" ] && [ "$FEE_AMT" != "0" ] || bad "無可用 fee 可結算"
R=$(J /api/rewardpool/topup "{\"asset\":\"TACm\",\"amount\":\"$FEE_AMT\"}")
echo "$R" | grep -q '"ok":true' && ok "topup 結算 fee→獎勵池（$FEE_AMT）" || bad "topup: $R"
R=$(curl -s "http://127.0.0.1:$BASE/api/exchange/fee")
echo "$R" | grep -q '"avail":"0"' && ok "結算後 fee 帳戶歸零" || bad "fee 歸零: $R"

# 3. 提取：獎勵池 → 地址。
P1=$(P)
[ -n "$P1" ] && ok "挹注後獎勵池餘額 $P1" || bad "池餘額查詢"
B0=$(curl -sf "http://127.0.0.1:$BASE/api/wallet/info?address=$U1" | python3 -c "import sys,json;print(json.load(sys.stdin)['tacm_raw'])")
R=$(J /api/rewardpool/withdraw "{\"to\":\"$U1\",\"amount\":\"$FEE_AMT\"}")
echo "$R" | grep -q '"ok":true' && ok "獎勵池提取成功" || bad "池提取: $R"
B1=$(curl -sf "http://127.0.0.1:$BASE/api/wallet/info?address=$U1" | python3 -c "import sys,json;print(json.load(sys.stdin)['tacm_raw'])")

# 4. 守恆（不隨區塊變動的證據）：
#    a) 提取後 U1 鏈上增加 == 提取額（vault 承擔轉帳費，池只扣 w+fee）；
#    b) 池至多扣 w+fee（多餘差值＝提取期間新區塊挹注）。
P2=$(P)
python3 -c "
import sys
p0=int('$P0'); p1=int('$P1'); p2=int('$P2'); b0=int('$B0'); b1=int('$B1')
from decimal import Decimal
w=int(Decimal('$FEE_AMT')*10**18)  # 結算金額（TACM→wei）
fee=w*2//100            # 2% 轉帳費（整數）
if (b1-b0)==w and p2>=p1-(w+fee) and (p1-p0)>=w: sys.exit(0)
print(f'pool wei: before={p0} after_topup={p1} after_withdraw={p2} withdrawn={w} fee={fee} u1_b0={b0} u1_b1={b1}')
sys.exit(1)
" && ok "獎勵池守恆（提取額全額到帳 U1；池至多扣 $FEE_AMT + 2% 費）" || bad "池守恆: before=$P0 after=$P1 withdraw=$P2"

# 5. 超額提取失敗：池餘額不足。
R=$(J /api/rewardpool/withdraw "{\"to\":\"$U1\",\"amount\":\"99999\"}")
echo "$R" | grep -q '"error"' && ok "超額池提取被拒" || bad "超額提取: $R"

echo
log "RESULT: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
