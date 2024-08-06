## 介绍
爬取v2ex和hacker news的贴子并通过tgbot推送到tg上

v2ex: 包含新贴推送和热帖标记

hacker news: 包含帖子推送和AI总结


## 使用方式
- **使用前提: telegram bot + deepseek api(如果要用hacker news中文总结)**

- 不想折腾可以直接加入[频道]，会推送v2ex和hacker news的热帖(https://t.me/news2tg)

### docker compose
~~~bash
# 1. 复制项目内的docker-compose.yml
nano docker-compose.yml

# 2. 创建挂载的文件夹
mkdir ./logs && mkdir ./config

# 3. 把项目内的配置文件放在config文件夹下
nano ./config/config.toml

# 4. 运行
docker compose up -d
~~~

### 自己构建
参考dockerfile文件

## todo
1. v2ex支持关键字
2. hackernews支持AI总结的开关
3. hackernews使用ollama 7b大模型