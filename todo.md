## TODO


2. 消息循环发送问题，如果有多条数据，同时发送的格式怎么处理?
    没啥好办法，暂时直接发，我只收集热帖



## DONE

1. tg api报错问题： 加一个转义方法 
~~~
Failed to send Telegram message: Api(Unknown("Bad Request: can't parse entities: Character '.' is reserved and must be escaped with the preceding '\\'"))
~~~