## TODO

### 加个hostloc热帖

### 增加个日志文件保存功能

### hacker news的AI总结用哪个大模型？

- 支持web搜索的?贵

用deepseek，便宜，方便注册，兼容openai


### 网页可能是视频，这总结个p阿？ 咋识别？

### 拿到的网页内容太多一次性post发送不了？

- 用文件的形式上传
    api不支持

- embedding后上传
    api不支持

- stream流式传输？
    扯蛋

- 精简内容
    



## DONE

### tg api报错问题： 加一个转义方法 
~~~
Failed to send Telegram message: Api(Unknown("Bad Request: can't parse entities: Character '.' is reserved and must be escaped with the preceding '\\'"))
~~~


### 消息循环发送问题，如果有多条数据，同时发送的格式怎么处理?
    没啥好办法，暂时直接发，我只收集热帖


### 为什么test的时候发送的链接可以预览，正常发送不行？
    暂时不管

## memo
1. 注意let mut shared_item1 = SharedItem::new(); 和 let shared_item2 = &mut SharedItem::new();的区别；
2. vscode 的debug真的难用
3. rustrover笨重, debug下环境变量没法设置就走不了代理，难受
