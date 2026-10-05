# ==========================================
# 阶段 1: 前端静态页面构建
# ==========================================
FROM node:20-alpine AS frontend-builder
WORKDIR /web
RUN apk add --no-cache git
ARG FRONTEND_REPO=https://github.com/carrot-hu23/dst-manage-web2.git
ARG FRONTEND_REF=main
RUN git clone --depth 1 -b ${FRONTEND_REF} ${FRONTEND_REPO} . && \
    npm config set registry https://registry.npmmirror.com && \
    npm ci && \
    npm run build

# ==========================================
# 阶段 2: Go 后端编译构建
# ==========================================
FROM golang:1.24-bookworm AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dst-admin-go cmd/server/main.go

# ==========================================
# 阶段 3: 最终运行镜像
# ==========================================
FROM debian:bookworm-slim

LABEL maintainer="hujinbo23 jinbohu23@outlook.com"
LABEL description="DoNotStarveTogether server panel written in golang."

RUN dpkg --add-architecture i386 && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
    curl \
    libcurl4-gnutls-dev:i386 \
    lib32gcc-s1 \
    lib32stdc++6 \
    libcurl4-gnutls-dev \
    libgcc-s1 \
    libstdc++6 \
    wget \
    ca-certificates \
    screen \
    procps \
    sudo \
    unzip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 拷贝二进制
COPY --from=backend-builder /app/dst-admin-go /app/dst-admin-go
RUN chmod 755 /app/dst-admin-go

# 拷贝 entrypoint 与配置
COPY scripts/docker/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod 755 /app/docker-entrypoint.sh
COPY config.yml /app/config.yml
COPY scripts/docker/docker_dst_config /app/dst_config
COPY scripts/docker/docker_dst_config /app/docker_dst_config.default

# 拷贝前端产物与静态资源
COPY --from=frontend-builder /web/dist /app/dist
COPY static /app/static

EXPOSE 8082/tcp
EXPOSE 10888/udp
EXPOSE 10998/udp
EXPOSE 10999/udp

ENTRYPOINT ["./docker-entrypoint.sh"]
