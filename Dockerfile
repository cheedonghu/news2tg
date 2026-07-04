# 使用多阶段构建：Go 构建器 + Python sidecar 运行时
FROM golang:1.22-alpine AS go-builder
WORKDIR /src

# 优先复制 go.mod / go.sum 以利用 layer 缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/news2tg ./cmd/news2tg

FROM python:3.11-slim
WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends git \
    && rm -rf /var/lib/apt/lists/*

# 从GitHub克隆Python仓库（HN 网页正文摘要服务）
RUN git clone https://github.com/cheedonghu/hacker-news-digest.git .

# 安装Python依赖
RUN pip install --no-cache-dir -r ./page_content_extractor/requirements.txt

# 从Go构建阶段复制编译好的二进制文件
COPY --from=go-builder /out/news2tg /app/news2tg

# 创建配置目录
RUN mkdir /config

# 沿用旧环境变量名以保持 docker-compose.yml 兼容
ENV RUST_CONFIG_PATH=/config/config.toml

# 暴露端口（其实没必要暴露）
EXPOSE 50051

# 启动命令：不再把输出重定向到文件，让日志走 stdout/stderr，
# 交给 Docker 的 logging driver 统一收集 + 轮转（见 docker-compose.yml 的 logging 段）。
CMD ["sh", "-c", "python -m page_content_extractor.main & ./news2tg -c $RUST_CONFIG_PATH"]
