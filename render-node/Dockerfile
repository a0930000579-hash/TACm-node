# TACm 主網節點（Render 用）— M147c / 兩套分離：節點系統＝nodeweb（無 DApp 前端）
# 兩階段建置：先編譯 Go，再放進精簡執行環境
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY src/ .
RUN go build -o /nodeweb ./cmd/nodeweb

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /nodeweb /usr/local/bin/nodeweb
EXPOSE 8080
# 純節點：同步＋驗證＋節點面板；p2p-url 自動依部署位置偵測（Render env）
CMD ["nodeweb", "-web-port", "8080", "-data-dir", "/var/tac/data", "-network", "mainnet", "-node-id", "node1", "-p2p", "-p2p-seed", "http://2.28.201.174:8080", "-p2p-follower"]
