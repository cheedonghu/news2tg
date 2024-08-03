# 使用多阶段构建
FROM rust:latest as rust-builder
WORKDIR /usr/src/news2tg
# 从GitHub克隆Rust仓库
RUN git clone https://github.com/cheedonghu/news2tg.git .
RUN cargo build --release

FROM python:3.11
WORKDIR /app

# 安装git
RUN apt-get update && apt-get install -y git

# 从GitHub克隆Python仓库
RUN git clone https://github.com/cheedonghu/hacker-news-digest.git .

# 安装Python依赖
RUN pip install --no-cache-dir -r ./page_content_extractor/requirements.txt

# 从Rust构建阶段复制编译好的二进制文件
COPY --from=rust-builder /usr/src/news2tg/target/release/news2tg /app/news2tg

# 创建配置文件目录
RUN mkdir /config

# 设置环境变量指向配置文件位置
ENV RUST_CONFIG_PATH=/config/rust-config.toml

# 暴露必要的端口（其实没必要暴露）
EXPOSE 11000

# 启动命令
CMD ["sh", "-c", "python -m page_content_extractor.main & ./news2tg"]