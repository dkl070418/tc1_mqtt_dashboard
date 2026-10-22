# ------ 构建阶段（始终在宿主平台编译，交叉输出目标架构二进制）------
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS builder
ARG TARGETARCH
ARG TARGETOS=linux
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o mqtt_mgr .

# ------ 运行阶段 ------
FROM alpine:latest
# 安装 docker CLI（用于 docker exec mosquitto_passwd）
RUN apk add --no-cache docker-cli
WORKDIR /app
COPY --from=builder /app/mqtt_mgr .
COPY config.yaml .
EXPOSE 8080
CMD ["./mqtt_mgr"]
