#!/usr/bin/env bash
cd "$(dirname "$0")"
for s in run_e2e_full.sh run_modules_rpc.sh run_exchange_smoke.sh run_wallet_smoke.sh run_m15_smoke.sh run_m16_smoke.sh run_m17_smoke.sh run_vc_smoke.sh run_mining_smoke.sh run_miner_smoke.sh run_pool_smoke.sh run_community_smoke.sh; do
  if [ -x "$s" ]; then
    echo "===== $s ====="
    bash "$s" > /tmp/reg_$s.log 2>&1
    tail -1 /tmp/reg_$s.log
    grep -E 'FAIL' /tmp/reg_$s.log | head -3
  fi
done
