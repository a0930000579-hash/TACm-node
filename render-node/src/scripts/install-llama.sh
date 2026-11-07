#!/usr/bin/env bash
# TACm Giant 本地模型一鍵安裝（llama.cpp + Qwen2.5-3B-Instruct GGUF）。
# 安裝後 tacweb/nodeweb 經 127.0.0.1:8081 自動偵測到 Giant 上線（TAC_LLAMA_PORT 可覆蓋埠）。
# 用法：sudo bash scripts/install-llama.sh
set -euo pipefail

LLAMA_DIR="${TAC_LLAMA_DIR:-/opt/tacm-llama}"
PORT="${TAC_LLAMA_PORT:-8081}"
LLAMA_VER="b11559"
# 中階開放權重模型：Qwen2.5-3B-Instruct Q4_K_M（約 2GB，多數 VPS 可順跑，明顯強於離線規則）。
MODEL_URL="https://huggingface.co/Qwen/Qwen2.5-3B-Instruct-GGUF/resolve/main/qwen2.5-3b-instruct-q4_k_m.gguf"
MODEL_FILE="$LLAMA_DIR/qwen2.5-3b-instruct-q4_k_m.gguf"

echo "[1/4] 建立目錄 $LLAMA_DIR"
mkdir -p "$LLAMA_DIR"

echo "[2/4] 下載 llama.cpp $LLAMA_VER (ubuntu x64)"
if [ ! -x "$LLAMA_DIR/llama-server" ]; then
  curl -fsSL -o /tmp/llama.tar.gz \
    "https://github.com/ggml-org/llama.cpp/releases/download/${LLAMA_VER}/llama-${LLAMA_VER}-bin-ubuntu-x64.tar.gz"
  rm -rf /tmp/llama-extract
  mkdir -p /tmp/llama-extract
  tar xzf /tmp/llama.tar.gz -C /tmp/llama-extract
  SRC="$(find /tmp/llama-extract -name llama-server -type f | head -1 | xargs dirname)"
  cp -a "$SRC/llama-server" "$SRC/llama-cli" "$LLAMA_DIR/"
  # 需含版本化函式庫（libxxx.so.0），故用 lib*.so* 並從整個解壓目錄蒐集。
  find /tmp/llama-extract -name 'lib*.so*' -exec cp -a {} "$LLAMA_DIR/" \;
  chmod +x "$LLAMA_DIR/llama-server" "$LLAMA_DIR/llama-cli"
fi

echo "[3/4] 下載 TACm Giant 模型 Qwen2.5-3B-Instruct (q4_k_m, 約 2GB)"
if [ ! -f "$MODEL_FILE" ]; then
  curl -fL --retry 3 -o "$MODEL_FILE" "$MODEL_URL"
fi

echo "[4/4] 註冊 systemd 服務 tacm-llama (127.0.0.1:$PORT)"
cat >/etc/systemd/system/tacm-llama.service <<EOF
[Unit]
Description=TACm Giant local LLM (llama.cpp)
After=network.target

[Service]
Type=simple
WorkingDirectory=$LLAMA_DIR
Environment=LD_LIBRARY_PATH=$LLAMA_DIR
ExecStart=$LLAMA_DIR/llama-server -m $MODEL_FILE -c 4096 --host 127.0.0.1 --port $PORT
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable tacm-llama >/dev/null 2>&1 || true
systemctl restart tacm-llama

echo -n "等待模型載入"
i=0
while [ "$i" -lt 30 ]; do
  if curl -sf "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    echo " OK"
    echo "TACm Giant 已就緒（http://127.0.0.1:$PORT）。重啟 tacnode 後模型清單即顯示 Giant 在線。"
    exit 0
  fi
  i=$((i+1))
  echo -n "."
  sleep 3
done
echo " 逾時：請檢查 systemctl status tacm-llama 與 journalctl -u tacm-llama"
exit 1
