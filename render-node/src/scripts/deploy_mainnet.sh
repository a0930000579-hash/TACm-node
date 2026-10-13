#!/usr/bin/env bash
# TAC 主網錨點一鍵部署：build + systemd 託管 + 防火牆
# 用法：sudo ./deploy_mainnet.sh --web-port 8080 --node-id mainnet1
set -euo pipefail

WEB_PORT="${WEB_PORT:-8080}"
NODE_ID="${NODE_ID:-mainnet1}"
DATA_DIR="${DATA_DIR:-/var/tac/data}"
BLOCK_TIME="${BLOCK_TIME:-2}"
DIFFICULTY="${DIFFICULTY:-3}"
P2P_URL="${P2P_URL:-}"
SEED="${SEED:-}"
FOLLOWER=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --web-port) WEB_PORT="$2"; shift 2;;
    --node-id)  NODE_ID="$2"; shift 2;;
    --data-dir) DATA_DIR="$2"; shift 2;;
    --block-time) BLOCK_TIME="$2"; shift 2;;
    --difficulty) DIFFICULTY="$2"; shift 2;;
    --p2p-url) P2P_URL="$2"; shift 2;;
    --seed) SEED="$2"; shift 2;;
    --follower) FOLLOWER=1; shift;;
    *) echo "未知參數: $1"; exit 1;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "請用 sudo 執行（需寫 /var/tac 與 systemd）。"
  exit 1
fi

echo "==> 1/4 建置 tacweb"
if ! command -v go >/dev/null 2>&1; then
  echo "未安裝 Go，請先：apt update && apt install -y golang-go"
  exit 1
fi
GOTOOLCHAIN=local go build -o tacweb ./cmd/web
echo "    建置完成"

echo "==> 2/4 準備資料目錄"
mkdir -p "$DATA_DIR"
chown -R "$SUDO_USER":"$SUDO_USER" "$DATA_DIR" 2>/dev/null || true

echo "==> 3/4 寫入 systemd 服務 /etc/systemd/system/tacnode.service"
ARGS="-web-port $WEB_PORT -rpc-port $WEB_PORT -data-dir $DATA_DIR -block-time $BLOCK_TIME -difficulty $DIFFICULTY"
if [[ -n "$P2P_URL" ]]; then
  ARGS="$ARGS -p2p -p2p-url $P2P_URL"
fi
if [[ -n "$SEED" ]]; then
  ARGS="$ARGS -p2p-seed $SEED"
fi
if [[ "$FOLLOWER" == "1" ]]; then
  ARGS="$ARGS -p2p-follower"
fi

cat > /etc/systemd/system/tacnode.service <<EOF
[Unit]
Description=TAC Mainnet Node
After=network.target

[Service]
WorkingDirectory=$(pwd)
ExecStart=$(pwd)/tacweb $ARGS
Restart=always
RestartSec=5
User=$SUDO_USER

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable tacnode
systemctl restart tacnode

echo "==> 4/4 防火牆開 $WEB_PORT"
if command -v ufw >/dev/null 2>&1; then
  ufw allow "$WEB_PORT"/tcp >/dev/null 2>&1 || true
  ufw status | head -5 || true
fi

echo
echo "部署完成！"
echo "  狀態：  systemctl status tacnode"
echo "  Logs：  journalctl -u tacnode -f"
echo "  驗證：  curl http://$(hostname -I | awk '{print $1}'):$WEB_PORT/status"
if [[ -z "$P2P_URL" ]]; then
  echo "  （未設 --p2p-url：此節點為孤立節點；要當錨點請加 --p2p-url http://<固定IP>:$WEB_PORT）"
fi
