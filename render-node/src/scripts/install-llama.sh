#!/usr/bin/env bash
# M102-4: 安裝 llama.cpp + Qwen2.5-0.5B GGUF（FlipOn / TAC AI Smart Chain 本地 AI 助手模型）
# 用途：在 Ubuntu x86_64 伺服器一鍵安裝本地大模型；安裝後 tacweb 的 /api/ai/ask 自動切到真實 AI 模型。
# 用法：sudo bash scripts/install-llama.sh
set -euo pipefail

LLAMA_DIR="${TAC_LLAMA_DIR:-/opt/tac-llama}"
PORT="${TAC_LLAMA_PORT:-8081}"
VER="b11559"

echo "[1/4] 建立目錄 $LLAMA_DIR"
mkdir -p "$LLAMA_DIR"

echo "[2/4] 下載 llama.cpp $VER (Ubuntu x86_64)"
if [ ! -x "$LLAMA_DIR/llama-server" ]; then
  curl -sL -o /tmp/llama-${VER}.tar.gz \
    "https://github.com/ggml-org/llama.cpp/releases/download/${VER}/llama-${VER}-bin-ubuntu-x64.tar.gz"
  rm -rf /tmp/llama-${VER}
  mkdir -p /tmp/llama-${VER}
  tar xzf /tmp/llama-${VER}.tar.gz -C /tmp/llama-${VER}
  SRC=$(find /tmp/llama-${VER} -name llama-server -type f | head -1 | xargs dirname)
  cp "$SRC/llama-server" "$SRC/llama-cli" "$LLAMA_DIR/"
  cp "$SRC"/lib*.so "$LLAMA_DIR/" 2>/dev/null || true
  chmod +x "$LLAMA_DIR/llama-server" "$LLAMA_DIR/llama-cli"
fi

echo "[3/4] 下載 Qwen2.5-0.5B-Instruct (q4_k_m, ~470MB)"
if [ ! -f "$LLAMA_DIR/qwen-0.5b.gguf" ]; then
  curl -sL -o "$LLAMA_DIR/qwen-0.5b.gguf" \
    "https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf"
fi

echo "[4/4] 註冊 systemd 服務 tac-llama (port $PORT)"
cat >/etc/systemd/system/tac-llama.service <<EOF
[Unit]
Description=TAC FlipOn local LLM (llama.cpp)
After=network.target

[Service]
ExecStart=$LLAMA_DIR/llama-server -m $LLAMA_DIR/qwen-0.5b.gguf -c 2048 --host 127.0.0.1 --port $PORT
WorkingDirectory=$LLAMA_DIR
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable tac-llama >/dev/null 2>&1 || true
systemctl restart tac-llama

echo -n "等待模型載入…"
for i in $(seq 1 20); do
  if curl -sf "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    echo " OK"
    curl -s "http://127.0.0.1:$PORT/health"
    echo
    echo "本地 AI 模型已就緒。tacweb 會自動偵測並使用（可用 TAC_LLAMA_PORT 覆蓋埠）。"
    exit 0
  fi
  sleep 3
done
echo " 逾時：請檢查 systemctl status tac-llama 與日誌 journalctl -u tac-llama"
exit 1
