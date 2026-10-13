#!/usr/bin/env bash
# ============================================================
# TAC Ai Smart Chain — 一鍵節點安裝器（人人可搭，三平台通用）
#
# 支援三種環境（自動偵測，或手動指定）：
#   1) Linux 伺服器 / 本機（Ubuntu/Debian）→ systemd 服務，開機自啟
#   2) 手機 Termux                → nohup 背景執行
#   3) Render / 雲平台            → 前台執行（用平台提供的 $PORT）
#
# 用法：
#   bash tacnet-node.sh [--port 8081] [--seed http://2.28.201.174:8080] [--mode auto|systemd|nohup|render]
#   --p2p-url 省略時自動偵測本機公網 IP（偵測失敗才需手動指定）
#
# 一行安裝：
#   curl -L -o /tmp/tacnet-node.sh '<INSTALLER_AKA>' && bash /tmp/tacnet-node.sh
# ============================================================
set -euo pipefail
cd "$(dirname "$0")"

# ---- 可調參數 ----
ZIP_URL='https://aka.doubaocdn.com/s/WLUKjNHxdu'      # M101 壓縮檔（錢包多幣種＋交易所 K 線/深度圖/個人成交）
SEED='http://2.28.201.174:8080'                        # 主網錨點（種子）
PORT="${1:-8081}"
MODE="${MODE:-auto}"
NETWORK="${NETWORK:-mainnet}"                          # mainnet|testnet
DATA_DIR="${DATA_DIR:-$HOME/tac_node_data}"
SERVICE='tacnode-ui'
# testnet 使用獨立資料目錄與鏈 ID，與主網完全分離
[ "$NETWORK" = "testnet" ] && DATA_DIR="${DATA_DIR}_testnet" && 

# 解析參數
while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="$2"; shift 2;;
    --seed) SEED="$2"; shift 2;;
    --mode) MODE="$2"; shift 2;;
    --p2p-url) P2P_URL="$2"; shift 2;;
    --network) NETWORK="$2"; shift 2;;
    *) shift;;
  esac
done

say()  { printf '\n\033[1;32m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
fail() { printf '\n\033[1;31m[錯誤]\033[0m %s\n' "$*"; exit 1; }

# ---- 1) 環境偵測 ----
detect_mode() {
  if [ "$MODE" != "auto" ]; then echo "$MODE"; return; fi
  if command -v termux-info >/dev/null 2>&1; then echo "nohup"          # Termux
  elif [ -d /run/systemd/system ]; then echo "systemd"                    # Linux systemd
  elif [ -n "${RENDER:-}" ] || [ -n "${PORT:-}" ] && [ -n "${RENDER_EXTERNAL_URL:-}" ]; then echo "render"
  else echo "nohup"; fi
}
MODE="$(detect_mode)"
say "環境模式：$MODE （埠 $PORT）"

# ---- 2) 依賴：Go ----
if ! command -v go >/dev/null 2>&1; then
  say "未找到 Go，開始安裝…"
  if command -v apt-get >/dev/null 2>&1; then
    sudo apt-get update -y && sudo apt-get install -y golang-go
  elif command -v pkg >/dev/null 2>&1; then
    pkg update -y && pkg install -y golang
  else
    fail "請先安裝 Go（https://go.dev/dl/），再重新執行本腳本。"
  fi
fi
GOBIN="$(command -v go)" || fail "Go 安裝失敗"

# ---- 3) 取得原始碼 ----
SRC="$HOME/tacm-go"
if [ ! -d "$SRC" ]; then
  say "下載 TAC 節點原始碼…"
  cd "$HOME"
  curl -L -o /tmp/tacm-go-m88.zip "$ZIP_URL" || fail "下載失敗，請檢查網路後重試"
  unzip -o -q /tmp/tacm-go-m88.zip -d "$HOME" || fail "解壓失敗"
fi
cd "$SRC"

# ---- 4) 編譯 ----
say "編譯 tacweb + nodeweb…"
GOTOOLCHAIN=local go build -o tacweb ./cmd/web
GOTOOLCHAIN=local go build -o nodeweb ./cmd/nodeweb
[ -x ./nodeweb ] || fail "編譯失敗"

# ---- 5) 公網 IP 偵測（供 p2p-url）----
detect_ip() {
  for u in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
    local ip
    ip="$(curl -s -m 5 "$u" || true)"
    case "$ip" in
      *[0-9].[0-9]*.[0-9]*.[0-9]*) echo "$ip"; return 0;;
    esac
  done
  echo ""
}
P2P_URL="${P2P_URL:-}"
if [ -z "$P2P_URL" ]; then
  IP="$(detect_ip)"
  if [ -n "$IP" ]; then P2P_URL="http://$IP:$PORT"; say "偵測公網 IP：$IP → P2P 基址 $P2P_URL"
  else P2P_URL="http://127.0.0.1:$PORT"; say "無法偵測公網 IP，暫用 $P2P_URL（遠端連入請改為實際公網 IP）"; fi
fi

# ---- 6) 依模式啟動 ----
case "$MODE" in
  render)
    # Render / 雲平台：前台執行；平台注入 $PORT。
    [ -n "${PORT:-}" ] && PORT_OVERRIDE="${PORT:-8081}"
    say "Render 模式：啟動 nodeweb（前台，Ctrl+C 停止）"
    exec ./nodeweb -web-port "${PORT_OVERRIDE:-8081}" \
      -p2p-url "$P2P_URL" -p2p-seed "$SEED" -p2p-follower \
      -data-dir "$DATA_DIR"
    ;;

  systemd)
    say "寫入 systemd 服務 $SERVICE…"
    sudo tee /etc/systemd/system/$SERVICE.service >/dev/null <<EOF
[Unit]
Description=TAC Ai Smart Chain - Node UI
After=network.target

[Service]
WorkingDirectory=$SRC
ExecStart=$SRC/nodeweb -web-port $PORT -p2p-url $P2P_URL -p2p-seed $SEED -p2p-follower -data-dir $DATA_DIR
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    sudo systemctl daemon-reload
    sudo systemctl enable --now $SERVICE
    sleep 3
    if curl -s -m 5 "http://127.0.0.1:$PORT/api/status" >/dev/null 2>&1; then
      say "✅ 節點已啟動（systemd 開機自啟）"
    else
      say "⚠️ 服務已建立但狀態尚未就緒，請查看：sudo systemctl status $SERVICE"
    fi
    ;;

  nohup)
    # Termux / 本機無 systemd：nohup 背景執行。
    mkdir -p "$DATA_DIR"
    say "nohup 模式：啟動 nodeweb（背景）"
    nohup ./nodeweb -web-port "$PORT" \
      -p2p-url "$P2P_URL" -p2p-seed "$SEED" -p2p-follower \
      -data-dir "$DATA_DIR" > "$HOME/tacnode-ui.log" 2>&1 &
    sleep 3
    if curl -s -m 5 "http://127.0.0.1:$PORT/api/status" >/dev/null 2>&1; then
      say "✅ 節點已啟動（背景執行，日誌：$HOME/tacnode-ui.log）"
    else
      say "⚠️ 啟動中，請稍候查看日誌：tail -f $HOME/tacnode-ui.log"
    fi
    ;;
esac

# ---- 7) 結果 ----
say "完成！"
echo
echo "  📱 節點品牌控制台：${P2P_URL#http://}"
echo "  🌐 主網入口：        http://2.28.201.174:8080"
echo "  📄 驗證狀態：        curl -s http://127.0.0.1:$PORT/api/status"
echo
