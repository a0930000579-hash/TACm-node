# TAC Ai Smart Chain — 預編譯 nodeweb 節點（follower 跟隨模式）
# 空庫啟動時自動從 anchor 快照秒級引導（快照含全鏈累計已發 TACm），
# 之後只經 P2P 同步、不主動出塊。
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY nodeweb /app/nodeweb
RUN chmod +x /app/nodeweb
EXPOSE 8080
CMD ["./nodeweb", "-web-port", "8080", "-data-dir", "/var/tac/data", "-network", "mainnet", "-block-time", "12", "-p2p", "-p2p-seed", "http://2.28.201.174:8080", "-p2p-follower"]
