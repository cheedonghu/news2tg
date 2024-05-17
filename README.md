## 介绍
爬取v2ex和hacker news的新贴并通过tgbot推送到tg上

v2ex: 包含新贴推送和热帖标记，支持关键字标记

hacker news: 包含帖子推送和AI总结

test

## 架构

### v2ex

~~数据爬取模块：~~ 改用api调用

api调用模块：reqwest

IP池模块?:

线程池模块:

数据解析模块: serde

数据推送模块：teloxide


### hacker news

数据爬取模块：

数据解析模块: 

数据推送模块：

AI交互模块：
