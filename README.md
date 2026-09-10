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

### 每日天气私聊

设置 `[features] weather_enabled = true` 后，每天按 `weather_push_time`（北京时间，默认 `07:00`）
向 `weather_mention` 中的用户逐个私聊发送天气，不发送到 `[telegram] chat_id` 配置的频道。
例如 `weather_mention = ["123456789:老王", "234567890"]`，仍兼容原来的显示名写法。
请填写正数用户 ID；重复 ID 只发一次，空列表不发送。用户需先与机器人开始对话。
单个用户发送失败会记录日志，继续发送给其他用户。启动时已过当天推送时间，会等待次日，不补发。

天气数据源为和风天气，使用 Ed25519 JWT 认证。启用天气时需要配置 `[qweather]` 的
`api_host`、`developer_id`、`project_id`、`key_id`、`private_key_path`，字段示例见 `config.toml`。
私钥使用 PKCS8 PEM 格式，公钥需事先上传和风控制台。关闭天气时不读取私钥。
城市编码仍写在 `weather_cities`，通过 GeoAPI 查询坐标后调用新版 `/weather/v1/daily`，
按北京时间匹配当天日期。每城市每轮调用两次 API（城市查询、天气预报）。

Docker 部署时，将私钥放在服务器 `./config/ed25519-private.pem`，配置路径填写
`/config/ed25519-private.pem`；compose 将整个配置目录只读挂载到 `/config`。
私钥不要提交 Git 或放入镜像。和风凭据的 API 限制应允许城市搜索和每日天气预报，
应用限制需允许运行机器；`403 Security Restriction` 表示请求被安全限制拒绝。

## todo
