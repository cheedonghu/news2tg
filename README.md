## 介绍
爬取v2ex和hacker news的贴子并通过tgbot推送到tg上

v2ex: 包含新贴推送和热帖标记

hacker news: 包含帖子推送和AI总结


## 使用方式
- **使用前提: telegram bot + deepseek api(如果要用hacker news中文总结)**

- 不想折腾可以直接加入[频道](https://t.me/news2tg)，会推送v2ex和hacker news的热帖

### docker compose
~~~bash
# 1. 复制项目内的docker-compose.yml
nano docker-compose.yml

# 2. 创建挂载的文件夹（日志由 Docker 收集，不再需要 ./logs）
mkdir ./config

# 3. 把项目内的配置文件放在config文件夹下
nano ./config/config.toml

# 4. 运行
docker compose up -d
~~~

升级注意：`[deepseek]` 段新增必填项 `model` 与 `agent_model`，老配置不补会启动失败并报 `[deepseek] model 未配置`（`docker logs` 里能搜到）。docker-compose 挂载的是本地 `config.toml`，拉新镜像不会自动带上新字段，升级前请手动把这两项加进配置。

### 自己构建
参考dockerfile文件

本地构建（Go ≥ 1.22）：
~~~bash
go mod tidy
go build -o bin/news2tg ./cmd/news2tg
./bin/news2tg -c config.toml
~~~

## 技术栈
本项目已从 Rust 重构为 Go：
- 主程序：Go（goroutine + `time.Ticker` 替代 `tokio`）
- 依赖：`go-telegram-bot-api/v5`、`sashabaranov/go-openai`（DeepSeek 兼容 OpenAI 协议）、`PuerkitoBio/goquery`、`BurntSushi/toml`
- HN 正文提取：仍由 Python sidecar [hacker-news-digest](https://github.com/cheedonghu/hacker-news-digest) 提供，通过 `127.0.0.1:50051` HTTP 调用
- Telegram 指令：`/summary <网址>` 总结网页并推送到频道；`/music <歌曲描述>` 从 mp3.pm
  搜索下载歌曲并经 WebDAV 上传到 alist 挂载的网盘，进度以单条消息原地编辑的方式实时回报。
  两条指令都仅限 `[telegram] admin_ids` 白名单用户。

## todo
