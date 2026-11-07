#!/usr/bin/env bash
# TAC 自主智能鏈 — Android APK（TWA 包裝）建構腳本。
# 依賴：Android SDK + JDK + @bubblewrap/cli（或 android-sdk 無頭環境）。
# 若環境齊備即自動建構；否則輸出完整手動步驟（不失敗退出，便於 CI/文件化）。
set -u
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

echo "[pack-apk] TAC 自主智能鏈 APK 建構"
echo "[pack-apk] 檢查依賴…"

HAS_BWR=0; HAS_ADB=0; HAS_JDK=0; HAS_GRADLE=0
command -v npx >/dev/null 2>&1 && npx --no-install @bubblewrap/cli --version >/dev/null 2>&1 && HAS_BWR=1
command -v adb >/dev/null 2>&1 && HAS_ADB=1
command -v java >/dev/null 2>&1 && HAS_JDK=1
command -v gradle >/dev/null 2>&1 && HAS_GRADLE=1

echo "bubblewrap=$HAS_BWR adb=$HAS_ADB jdk=$HAS_JDK gradle=$HAS_GRADLE"

# 先建構 PWA 產物（web 二進位內嵌，無需額外步驟）。
if [ ! -x ./tacweb ]; then
  GOTOOLCHAIN=local go build -o tacweb ./cmd/web || { echo "web build failed"; exit 2; }
fi
echo "[pack-apk] PWA 建構完成：tacweb（/dashboard /wallet /exchange 均為可安裝 PWA）"

if [ "$HAS_BWR" = "1" ] && [ "$HAS_JDK" = "1" ]; then
  echo "[pack-apk] 自動建構 TWA（需要可達的 https 網址，見 DEPLOY.md 部署後執行）："
  echo "  npx @bubblewrap/cli init --manifest https://你的網址/manifest.webmanifest"
  echo "  npx @bubblewrap/cli build"
  echo "  產出 app-release-signed.apk → 安裝到 Android（Android 7+）。"
else
  cat <<'DOC'

────────────────────────────────────────────────────────
完整手動步驟（無頭環境缺 SDK 時使用；或直接採用 PWA 方案）:
1. 安裝依賴
   - JDK 17:  sudo apt install openjdk-17-jdk
   - Android SDK: 下載 commandlinetools，執行 sdkmanager "platform-tools" "platforms;android-34"
   - Bubblewrap:  npm i -g @bubblewrap/cli
2. 建構
   npx @bubblewrap/cli init --manifest https://你的網址/manifest.webmanifest
   npx @bubblewrap/cli build
3. 產物： app-release-signed.apk（TWA 包裝完整 PWA，含離線快取）
────────────────────────────────────────────────────────
PWA 方案（免 SDK，最簡）：手機 Chrome/Edge 開啟你的網址 →
右上選單「加到主螢幕」→ 以全螢幕 App 方式運行，功能與 APK 完全一致。
DOC
fi
echo "[pack-apk] 完成"
