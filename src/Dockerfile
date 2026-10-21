# ===== TAC 自主智能鏈 — 單節點容器 =====
# 多階段構建：Go 1.23 → Alpine 執行（最小 ~45MB 映像）。
# 用法：
#   docker build -t tac-chain .
#   docker run -d --name tac-chain -p 8080:8080 -v tac-data:/data -e DATA_DIR=/data tac-chain
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
RUN apk add --no-cache git ca-certificates tzdata \
 && GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tacweb ./cmd/web

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S tac && adduser -S tac -G tac
WORKDIR /app
COPY --from=build /out/tacweb /app/tacweb
USER tac
EXPOSE 8080
VOLUME ["/data"]
ENV DATA_DIR=/data
ENV PORT=8080
# Render 等平台注入 $PORT：Web/RPC 同埠（/api/* 即 RPC，前端同源直連）。
CMD ["sh", "-c", "exec ./tacweb -web-port $PORT -rpc-port $PORT -data-dir $DATA_DIR -block-time 1 -difficulty 1"]
