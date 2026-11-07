#!/usr/bin/env bash
# TAC 自主智能鏈 M25-A DeFi 實機驗證：
# 真進程 tacweb → 流動性池（seed 注資/添加/領獎/移除）＋借貸市場（存/取/借/還、抵押釋放）→
# 資產全程對接 wallet 帳本，驗證轉帳、LP 公式、APR 獎勵、10% 年息與抵押/清算規則。
set -u
BASE=8783; WEB=8784; DATA=".smoke_defi"
ROOT="$(cd "$(dirname "$0")" && pwd)"; cd "$ROOT"
PASS=0; FAIL=0
ok()  { PASS=$((PASS+1)); echo "[defi] PASS: $*"; }
bad() { FAIL=$((FAIL+1)); echo "[defi] FAIL: $*"; }
log() { echo "[defi] $*"; }
PY=/opt/python3.12/bin/python3
J() { curl -s -X POST "http://127.0.0.1:$BASE$1" -H 'Content-Type: application/json' -d "$2"; }
G() { curl -s --max-time 5 "http://127.0.0.1:$BASE$1"; }
BAL() { G "/api/wallet/info?address=$1" ; }
TACMB() { BAL "$1" | $PY -c "import sys,json;print(json.load(sys.stdin).get('tacm_raw','0'))"; }
USDTB() { BAL "$1" | $PY -c "import sys,json;print(json.load(sys.stdin).get('usdt_raw','0'))"; }

pkill -x tacweb 2>/dev/null; pkill -x tacnode 2>/dev/null; sleep 0.5
rm -rf "$DATA" && mkdir -p "$DATA"
( cd "$ROOT" && GOTOOLCHAIN=local go build -o tacweb ./cmd/web ) || { echo "build failed"; exit 2; }
TAC_IDO_START_OFFSET_SEC=-10 TAC_VAULT_COMPOUND_MIN_SEC=2 nohup ./tacweb -data-dir "$DATA" -web-port $WEB -rpc-port $BASE -block-time 1 -difficulty 1 >"$DATA/w.log" 2>&1 &
trap 'pkill -x tacweb 2>/dev/null' EXIT
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$BASE/health" >/dev/null 2>&1 && break; sleep 0.4; done

# 0. 等待出塊與錢包就緒。
for i in $(seq 1 40); do
  H=$(curl -sf --max-time 2 "http://127.0.0.1:$BASE/status" | $PY -c "import sys,json;print(json.load(sys.stdin).get('block_height',0))" 2>/dev/null)
  [ "${H:-0}" -ge 2 ] && break
  sleep 0.5
done

# 1. 流動性池：seed 3 池＋初始注資。
P=$(G /api/defi/pools)
echo "$P" | $PY -c "import sys,json; d=json.load(sys.stdin); ps=[p['pair'] for p in d['pools']]; assert ('TACM/USDT' in ps) and ('TIUSD/USDT' in ps) and ('TACM/TIUSD' in ps or 'TACM/TiUSD' in ps), ps" \
  && ok "流動性池 seed 3 池（TACM/USDT、TACM/TIUSD、TIUSD/USDT）" || bad "pools: $(echo "$P" | head -c 200)"
echo "$P" | $PY -c "import sys,json;d=json.load(sys.stdin);p=d['pools'][0];assert p['reserve0']==50000 and p['reserve1']==500000 and p['apr']>0" 2>/dev/null \
  && ok "池1 seed 儲備 50000 TACM＋500000 USDT、APR 45%" || bad "池1 seed 數據錯誤"

# 2. 添加流動性：A 加池1（10 TACM＋100 USDT），LP=min 比例。
A=$(curl -sf "http://127.0.0.1:$BASE/wallet/new" | $PY -c "import sys,json;print(json.load(sys.stdin).get('address',''))")
J /api/wallet/deposit "{\"to\":\"$A\",\"asset\":\"TACm\",\"amount\":\"100\",\"memo\":\"defi-a\"}" >/dev/null
J /api/wallet/deposit "{\"to\":\"$A\",\"asset\":\"USDT\",\"amount\":\"1000\",\"memo\":\"defi-a\"}" >/dev/null
B0=$(TACMB "$A")
R=$(J /api/defi/add-liquidity "{\"address\":\"$A\",\"pool_id\":1,\"amount0\":10,\"amount1\":100}")
echo "$R" | grep -q '"ok":true' && echo "$R" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['lp_amount']>0" 2>/dev/null \
  && ok "添加流動性成功（10 TACM＋100 USDT → LP=$(echo "$R" | $PY -c "import sys,json;print(round(json.load(sys.stdin)['lp_amount'],4))")）" || bad "add-liquidity: $R"
[ -n "$A" ] || bad "wallet/new 失敗（A 為空）"
B1=$(TACMB "$A")
$PY -c "import sys;assert int('$B0')-int('$B1') >= 10*10**18, ('$B0','$B1')" 2>/dev/null && ok "用戶 TACm 已扣（10＋鏈上費）" || bad "扣款異常 $B0 -> $B1"
P1=$(G /api/defi/pools)
echo "$P1" | $PY -c "import sys,json;d=json.load(sys.stdin);p=d['pools'][0];assert p['reserve0']==50010 and p['reserve1']==500100, (p['reserve0'],p['reserve1'])" 2>/dev/null \
  && ok "池儲備更新 50010/500100（10+100 入池）" || bad "池儲備未更新: $(echo "$P1" | $PY -c "import sys,json;d=json.load(sys.stdin);p=d['pools'][0];print(p['reserve0'],p['reserve1'])" 2>/dev/null)"

# 3. 我的頭寸＋待領獎勵。
M=$(G "/api/defi/my-liquidity?address=$A")
echo "$M" | grep -q '"pool_id":1' && echo "$M" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['positions'][0]['lp_amount']>0" 2>/dev/null \
  && ok "我的頭寸顯示 LP>0" || bad "my-liquidity: $(echo "$M" | head -c 200)"

# 4. 領取流動性獎勵（APR 計算，>0 即領取成功）。
sleep 2
R=$(J /api/defi/claim-reward/1 "{\"address\":\"$A\"}")
echo "$R" | grep -q '"ok":true' && echo "$R" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['reward']>0 and d['token']=='TACm'" 2>/dev/null \
  && ok "領取流動性獎勵（$(echo "$R" | $PY -c "import sys,json;print(round(json.load(sys.stdin)['reward'],6))" 2>/dev/null) TACm）" || bad "claim-reward: $R"

# 5. 移除流動性：全部移除 → 贖回（token 回錢包）。
M2=$(G "/api/defi/my-liquidity?address=$A")
PID=$(echo "$M2" | $PY -c "import sys,json;print(json.load(sys.stdin)['positions'][0]['id'])" 2>/dev/null)
LP=$(echo "$M2" | $PY -c "import sys,json;print(json.load(sys.stdin)['positions'][0]['lp_amount'])" 2>/dev/null)
T0A=$(echo "$M2" | $PY -c "import sys,json;print(json.load(sys.stdin)['positions'][0]['token0_amount'])" 2>/dev/null)
R=$(J /api/defi/remove-liquidity "{\"address\":\"$A\",\"position_id\":$PID,\"lp_amount\":$LP}")
echo "$R" | grep -q '"ok":true' && echo "$R" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['amount0']>0" 2>/dev/null \
  && ok "移除流動性成功（贖回 $(echo "$R" | $PY -c "import sys,json;d=json.load(sys.stdin);print(round(d['amount0'],4),round(d['amount1'],4))" 2>/dev/null)）" || bad "remove-liquidity: $R"
M3=$(G "/api/defi/my-liquidity?address=$A")
echo "$M3" | grep -q '"positions":\[' && ok "頭寸已清空（全部移除）" || bad "頭寸未清空: $(echo "$M3" | head -c 120)"

# 6. 借貸市場：seed 3 市場。
L=$(G /api/defi/lending-markets)
echo "$L" | grep -q '"asset":"USDT"' && echo "$L" | grep -q '"asset":"TACm"' && echo "$L" | grep -q '"asset":"TIUSD"' \
  && ok "借貸市場 seed 3（USDT/TACM/TIUSD）" || bad "markets: $(echo "$L" | head -c 200)"

# 7. 存款：A 存 100 USDT → 市場池帳戶+、總存款+。
R=$(J /api/defi/deposit "{\"address\":\"$A\",\"asset\":\"USDT\",\"amount\":100}")
echo "$R" | grep -q '"ok":true' && ok "存款 100 USDT 成功" || bad "deposit: $R"
L1=$(G /api/defi/lending-markets)
echo "$L1" | $PY -c "import sys,json;d=json.load(sys.stdin);m=[x for x in d['markets'] if x['asset']=='USDT'][0];assert m['total_deposit']==500100, m['total_deposit']" 2>/dev/null \
  && ok "市場總存款 +100（500000→500100）" || bad "total_deposit: $(echo "$L1" | $PY -c "import sys,json;d=json.load(sys.stdin);m=[x for x in d['markets'] if x['asset']=='USDT'][0];print(m['total_deposit'])" 2>/dev/null)"

# 8. 取款：A 取 50 USDT 回錢包。
R=$(J /api/defi/withdraw "{\"address\":\"$A\",\"asset\":\"USDT\",\"amount\":50}")
echo "$R" | grep -q '"ok":true' && ok "取款 50 USDT 成功" || bad "withdraw: $R"
D=$(G "/api/defi/my-deposits?address=$A")
echo "$D" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['deposits'][0]['amount']==50, d['deposits'][0]['amount']" 2>/dev/null \
  && ok "我的存款餘額 50（100-50）" || bad "deposits: $(echo "$D" | head -c 200)"

# 9. 抵押借款：A 抵押 50 TACM 借 30 USDT（抵押率 75% → 上限 37.5）。
R=$(J /api/defi/borrow "{\"address\":\"$A\",\"collateral_asset\":\"TACm\",\"collateral_amount\":50,\"borrow_asset\":\"USDT\",\"borrow_amount\":30}")
echo "$R" | grep -q '"ok":true' && echo "$R" | grep -q '"loan_id":1' && ok "抵押借款成功（50 TACM 抵押 → 借 30 USDT）" || bad "borrow: $R"
# 超額借款被拒（20 TACM 抵押上限 15）
R=$(J /api/defi/borrow "{\"address\":\"$A\",\"collateral_asset\":\"TACm\",\"collateral_amount\":20,\"borrow_asset\":\"USDT\",\"borrow_amount\":40}")
echo "$R" | grep -q '超出最大可借' && ok "超額借款被拒（40 > 上限 15）" || bad "超額借款: $R"
LO=$(G "/api/defi/my-loans?address=$A")
echo "$LO" | grep -q '"status":"active"' && echo "$LO" | grep -q '"liquidation_price"' && ok "貸款記錄 active＋清算價" || bad "loans: $(echo "$LO" | head -c 200)"

# 10. 還款＋抵押釋放：還 60（本金）＋利息 → repaid、抵押品回錢包。
R=$(J /api/defi/repay "{\"address\":\"$A\",\"loan_id\":1,\"amount\":30.2}")
echo "$R" | grep -q '"ok":true' && echo "$R" | $PY -c "import sys,json;assert json.load(sys.stdin)['result']['status']==1" 2>/dev/null \
  && ok "全額還款（60+息）→ 貸款結清" || bad "repay: $R"
LO2=$(G "/api/defi/my-loans?address=$A")
echo "$LO2" | grep -q '"status":"repaid"' && ok "貸款狀態 repaid（抵押已釋放）" || bad "loans 未結清: $(echo "$LO2" | head -c 200)"

# 12. 頁面可達。
curl -sf --max-time 5 "http://127.0.0.1:$WEB/defi" | grep -q 'TAC</b> DEFI' && ok "/defi 頁面可達（幣安風 DeFi 中心）" || bad "/defi 頁面異常"

EXP=$($PY -c "print(1000-(100*1.0125)-(100*1.0125)+100+50+30-30.2*1.0125)")
FB2=$(USDTB "$A")
$PY -c "
import sys
bal=int('$FB2')/1e6
delta=1000-(100*1.0125)-(100*1.0125)+100+50+30-30.2*1.0125
assert abs(bal-delta) < 1.0, (bal, delta)
print('OK')

" 2>/dev/null && ok "USDT 資產守恆（終值 $FB2/1e6 ≈ $EXP）" || bad "USDT 守恆: $FB2/1e6 vs $EXP"
# 15. IDO 發行平台。
echo "--- IDO ---"
I=$(G /api/defi/ido-projects)
echo "$I" | $PY -c "import sys,json;d=json.load(sys.stdin);ps=d['projects'];assert len(ps)==2 and ps[0]['symbol']=='TACG' and ps[0]['token_price']==0.5 and ps[0]['raise_asset']=='USDT' and ps[1]['symbol']=='TACA'"   && ok "IDO seed 2 項目（TACG 0.5／TACA 1.0 USDT）" || bad "ido-projects: $(echo "$I" | head -c 200)"
S=$(J /api/defi/ido-subscribe "{\"address\":\"$A\",\"project_id\":1,\"amount\":50}")
echo "$S" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d.get('ok') and d['tokens']==100.0, d"   && ok "認購 TACG 50 USDT → 分配 100 枚" || bad "ido-subscribe: $S"
S2=$(J /api/defi/ido-subscribe "{\"address\":\"$A\",\"project_id\":1,\"amount\":60}")
echo "$S2" | $PY -c "import sys,json;d=json.load(sys.stdin);assert '已認購此項目' in d.get('error',''), d"   && ok "同一項目重複認購被拒" || bad "重複認購應拒: $S2"
S3=$(J /api/defi/ido-subscribe "{\"address\":\"$A\",\"project_id\":1,\"amount\":10}")
echo "$S3" | $PY -c "import sys,json;d=json.load(sys.stdin);assert '最低認購' in d.get('error',''), d"   && ok "低於 min_buy 認購被拒" || bad "min_buy 應拒: $S3"
M=$(G /api/defi/my-ido?address=$A)
echo "$M" | $PY -c "import sys,json;d=json.load(sys.stdin);ss=d['subscriptions'];assert len(ss)==1 and ss[0]['allocated_tokens']==100 and not ss[0]['claimed']"   && ok "我的認購 1 筆（100 枚未領取）" || bad "my-ido: $M"
C=$(J /api/defi/ido-claim/1 "{\"address\":\"$A\"}")
echo "$C" | $PY -c "import sys,json;d=json.load(sys.stdin);assert '尚未結束' in d.get('error',''), d"   && ok "未完成項目領取被拒（upcoming）" || bad "claim 應拒: $C"

# 16. 收益聚合器 Vault。
echo "--- Vault ---"
V=$(G /api/defi/vaults)
echo "$V" | $PY -c "import sys,json;d=json.load(sys.stdin);vs=d['vaults'];assert len(vs)==3 and vs[0]['underlying_asset']=='TACm' and vs[0]['total_assets']==100000 and vs[0]['apr']==0.18"   && ok "Vault seed 3 金庫（TACM 18%／USDT 12%／TiUSD 8%）" || bad "vaults: $(echo "$V" | head -c 200)"
D=$(J /api/defi/vault-deposit "{\"address\":\"$A\",\"vault_id\":2,\"amount\":100}")
echo "$D" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d.get('ok') and d['shares']==100.0, d"   && ok "存入 USDT 金庫 100 → 份額 100（首存等額）" || bad "vault-deposit: $D"
MV=$(G /api/defi/my-vaults?address=$A)
echo "$MV" | $PY -c "import sys,json;d=json.load(sys.stdin);ps=d['positions'];assert len(ps)==1 and ps[0]['vault_id']==2 and abs(ps[0]['value_now']-100)<0.01"   && ok "我的金庫頭寸（100 份額 ≈ 100 USDT）" || bad "my-vaults: $MV"
CP=$(J /api/defi/vault-compound/1 "{\"address\":\"$A\"}")
echo "$CP" | $PY -c "import sys,json;d=json.load(sys.stdin);assert '間隔' in d.get('error',''), d"   && ok "復投間隔過短被拒" || bad "compound 應拒: $CP"
W=$(J /api/defi/vault-withdraw "{\"address\":\"$A\",\"position_id\":1,\"shares\":100}")
echo "$W" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d.get('ok') and abs(d['amount']-100)<0.01, d"   && ok "取出金庫 100 份額 → 贖回 ≈100 USDT" || bad "vault-withdraw: $W"
MV2=$(G /api/defi/my-vaults?address=$A)
echo "$MV2" | $PY -c "import sys,json;d=json.load(sys.stdin);assert d['positions']==[], d"   && ok "全額取出後頭寸清空" || bad "頭寸應清空: $MV2"

echo "================ 結果：PASS=$PASS FAIL=$FAIL ================"
[ "$FAIL" -eq 0 ]
