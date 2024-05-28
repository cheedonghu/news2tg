## TODO

### hacker news的AI总结用哪个大模型？

- 支持web搜索的?

- 普通的?




## DONE

### tg api报错问题： 加一个转义方法 
~~~
Failed to send Telegram message: Api(Unknown("Bad Request: can't parse entities: Character '.' is reserved and must be escaped with the preceding '\\'"))
~~~


### 消息循环发送问题，如果有多条数据，同时发送的格式怎么处理?
    没啥好办法，暂时直接发，我只收集热帖



## memo
1. 注意let mut shared_item1 = SharedItem::new(); 和 let shared_item2 = &mut SharedItem::new();的区别；
2. 