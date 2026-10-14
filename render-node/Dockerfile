# TACm 主網節點（Render 用）— 預編譯二進位方案
# 不需在 Render 編譯源碼（避免上傳完整性問題）；nodeweb 為 Linux amd64 靜態二進位
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY nodeweb /usr/local/bin/nodeweb
RUN chmod +x /usr/local/bin/nodeweb
# 供應模型與錨點一致：annual_decay（總上限 52,003,300、6 年、年衰減 25%）
ENV TACM_MAX_SUPPLY=52003300 TACM_EMISSION_YEARS=6 TACM_ANNUAL_DECAY_PCT=0.25
EXPOSE 8080
# 純節點：同步＋驗證＋節點面板；p2p-url 自動依部署位置偵測（Render env）
CMD ["nodeweb", "-web-port", "8080", "-data-dir", "/var/tac/data", "-network", "mainnet", "-block-time", "3", "-p2p", "-p2p-seed", "http://2.28.201.174:8080", "-p2p-follower"]
