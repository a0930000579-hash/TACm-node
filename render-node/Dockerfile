# TACm 主網節點（Render 用）— M147c
# 兩階段建置：先編譯 Go，再放進精簡執行環境
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY src/ .
RUN go build -o /tacweb ./cmd/web

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /tacweb /usr/local/bin/tacweb
EXPOSE 8080
CMD ["tacweb", "-web-port", "8080", "-rpc-port", "8080", "-block-time", "3", "-data-dir", "/var/tac/data", "-network", "mainnet", "-p2p", "-p2p-url", "https://tacm-node.onrender.com", "-p2p-seed", "http://2.28.201.174:8080", "-serve-dapp=false", "-p2p-follower", "-bridge"]
