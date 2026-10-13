#!/usr/bin/env bash
# TAC 自主智能鏈 — iOS 主螢幕 Web App（近似 IPA 體驗）指引。
# iOS 不允許非 App Store 直接安裝 IPA；官方路徑＝PWA（免簽名）。
set -u
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
echo "[pack-ipa] TAC 自主智能鏈 iOS 交付"
echo "[pack-ipa] 官方路徑＝PWA（iOS 13+ 原生支援）："
cat <<'DOC'
────────────────────────────────────────────────────────
iOS 主螢幕安裝（免 App Store）：
1. Safari 開啟你的網址（需 https，見 DEPLOY.md）。
2. 分享鈕 →「加入主畫面」→ 命名 TAC。
3. 從主螢幕圖示開啟＝全螢幕獨立 App（含離線快取 SW）。
────────────────────────────────────────────────────────
若要正式 .ipa（App Store/TestFlight）：需 Apple Developer 帳號 + Xcode 包裝
WKWebView 指向網址（Cordova/Capacitor 均可），此流程需 Mac 與付費帳號，
屬發行階段，非開源部署範疇。
