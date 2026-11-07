#!/bin/bash
# ============================================================
# TACm Node 一鍵安裝/更新腳本（v74）
# 用法（架設者任選其一）：
#   bash <(curl -fsSL http://<主鏈>/downloads/install-node.sh)            # 一鍵安裝
#   bash <(curl -fsSL http://<主鏈>/downloads/install-node.sh) update      # 一鍵更新到最新版
#   參數：$1=update（更新模式）或 下載來源 DL；$2=種子節點；$3=Web 埠；$4=資料目錄
#   更新模式：自動備份目前 nodeweb → 下載最新（主鏈/Render 多來源）→ 覆蓋 → 重啟 tacnode
# ============================================================
set -euo pipefail

DL="${1:-http://2.28.201.174:8080/downloads,https://tacm-ai-cyprto.onrender.com/downloads}"
SEED="${2:-http://2.28.201.174:8080}"
WEB_PORT="${3:-8081}"
DATA_DIR="${4:-/var/tac/data}"

# sudo 相容（root 直接跑時無 sudo 也行）
SUDO=""
command -v sudo >/dev/null 2>&1 && SUDO="sudo"

# v71：多來源 fallback——DL 可為逗號分隔（主鏈→Render），依序嘗試直到成功，
# 避免主鏈外部 502 時架設者下載不到。
fetch() {
  local IFS=','
  for u in $DL; do
    u=$(echo "$u" | xargs)
    [ -z "$u" ] && continue
    if command -v curl >/dev/null 2>&1 && curl -fsSL "$u/$1" -o "$2" 2>/dev/null; then
      return 0
    fi
    if command -v wget >/dev/null 2>&1 && wget -qO "$2" "$u/$1" 2>/dev/null; then
      return 0
    fi
  done
  echo "✗ 無法從任何來源下載 $1（已嘗試：$DL）。"
  echo "  請確認網路可達主鏈/Render，或手動下載 nodeweb 後放置到 /usr/local/bin/。"
  return 1
}

# v74：一鍵更新模式——備份→下載最新 nodeweb（多來源）→覆蓋→重啟。
if [ "${1:-}" = "update" ]; then
  echo "→ TACm 節點一鍵更新："
  fetch nodeweb /tmp/tacm-nodeweb.new || exit 1
  BIN=/usr/local/bin/nodeweb
  if [ ! -f "$BIN" ]; then
    echo "✗ 未找到 $BIN（此機尚未安裝節點）。請先執行一鍵安裝。"
    exit 1
  fi
  BAK="$BIN.bak.$(date +%Y%m%d%H%M%S)"
  $SUDO cp "$BIN" "$BAK" && echo "→ 已備份：$BAK"
  $SUDO mv /tmp/tacm-nodeweb.new "$BIN"
  $SUDO chmod +x "$BIN"
  echo "→ 新版本：$(ls -la "$BIN" | awk '{print $5}') bytes"
  if systemctl list-unit-files 2>/dev/null | grep -q tacnode; then
    $SUDO systemctl restart tacnode && echo "✅ tacnode 已重啟"
  else
    echo "→ 未找到 tacnode.service；請手動重啟你的節點服務。"
  fi
  echo "✅ 更新完成。檢查：systemctl status tacnode"
  exit 0
fi

echo "→ 從 $DL 下載 nodeweb ..."
fetch "$DL/nodeweb" /usr/local/bin/nodeweb
$SUDO chmod +x /usr/local/bin/nodeweb
echo "✓ nodeweb 已安裝（$(stat -c%s /usr/local/bin/nodeweb 2>/dev/null || echo '?') bytes）"

$SUDO mkdir -p "$DATA_DIR"
PUBIP=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -z "$PUBIP" ] && PUBIP="127.0.0.1"

$SUDO tee /etc/systemd/system/tacnode.service >/dev/null <<SVC
[Unit]
Description=TACm Node
After=network.target
[Service]
ExecStart=/usr/local/bin/nodeweb -data-dir $DATA_DIR -web-port $WEB_PORT -p2p -p2p-url http://$PUBIP:$WEB_PORT -p2p-seed $SEED -snapshot-url $SEED
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
SVC

$SUDO systemctl daemon-reload
$SUDO systemctl enable --now tacnode
echo "=============================================="
echo "✓ TACm 節點安裝完成"
echo "  狀態：systemctl status tacnode"
echo "  日誌：journalctl -u tacnode -f"
echo "  網頁：http://$PUBIP:$WEB_PORT"
echo "=============================================="
