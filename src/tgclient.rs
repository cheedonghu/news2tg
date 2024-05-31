use teloxide::prelude::*;
use teloxide::types::ParseMode;
use tokio::runtime::Runtime;
use tokio::time::{Duration, sleep};
use crate::tools::*;


pub struct TgClient {
    pub bot: Bot,
    pub chat_id: ChatId,
}

impl TgClient {
    pub fn new(bot: Bot,chat_id: ChatId)-> Self{
        TgClient { bot, chat_id}
    }

    /// tg推送，text限制在4096,内容需要转义
    pub async fn send_message(&self, text: &str) -> Result<(), teloxide::RequestError> {
        // let truncated_text=truncate_utf8(text, 4096);
        // 转义特殊字符
        // let escaped_text=escape_markdown_v2(&truncated_text);
        self.bot.send_message(self.chat_id, text)
            .parse_mode(ParseMode::MarkdownV2)
            .send()
            .await?;
        Ok(())
    }

    /// 分批次发送消息
    pub async fn send_batch_message(&self, message_array: &Vec<String>) -> Result<(), teloxide::RequestError> {
        // 这里循环发可能触发tg限制 todo
        for message in message_array{
            println!("{}",message);
            // 不显示预览： 1.网页元数据问题 2.频率太快？
            self.bot.send_message(self.chat_id, message)
                .parse_mode(ParseMode::MarkdownV2).disable_web_page_preview(false)
                .send()
                .await?;

            // 停顿1.5s防止预览不显示
            sleep(Duration::from_millis(1500)).await;
        }
        Ok(())
    }

    pub async fn send_telegram_message_html(&self, text: &str) -> Result<(), teloxide::RequestError> {
        let truncated_text=truncate_utf8(text, 4096);
        // 转义特殊字符
        // let escaped_text=escape_markdown_v2(&truncated_text);
        self.bot.send_message(self.chat_id, truncated_text)
            .parse_mode(ParseMode::Html)
            .send()
            .await?;
        Ok(())
    }


}


#[cfg(test)]
mod tests{
    use std::error::Error;

    use super::*;
    use crate::config::Config;

    #[test]
    fn test_send_telegram_message() -> Result<(), Box<dyn Error>>{
        let config = Config::from_file("config.toml");
        let bot = Bot::new(&config.telegram.api_token);
        let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));
        let tgclient=TgClient::new(bot, chat_id);

        let new_runtime = Runtime::new()?;
        // 同步执行
        new_runtime.block_on(async {
            let title=String::from("测试title");
            let mut message_arrary = Vec::new();
            // message_arrary.push(format!("*热帖推送*: [{}]({})\n", title, "luyublog.com"));
            message_arrary.push(String::from(
                r"*Hacker News Top推送*: 
                Comment Site: https://news\.ycombinator\.com/item?id\=40530365
                AI总结: 待定
               [源内容: ](https://vickiboykis.com/2024/05/20/dont-worry-about-llms/)"));


            // message_arrary.push(
            //     format!("*Hacker News Top推送*: \n Comment Site:{}\n {}\n[{}]({})", "luyublog\\.com" , "AI总结: 待定","源内容: ", "https://greptime.com/blogs/2024-05-21-fault-tolerance"));
        

            // message_arrary.push(String::from(r"*热帖推送*: [title](luyublog.com)"));
            
            
            tgclient.send_batch_message(&message_arrary).await.unwrap();

            

            // message_arrary.push(&format!("<a href=\"{}\">*热帖推送*: {}</a>\n", "luyublog.com", title));
            // message.push_str("<pre> </pre> ");
            // message.push_str(&format!("<a href=\"{}\">*热帖推送*: {}</a>\n", "luyublog.com", title));
            

            // message.push_str(&format!("*主 题*: [{}]({})\n", topic.title, topic.url));
            // tgclient.send_telegram_message_html(&message).await.unwrap();
            // match tgclient.send_telegram_message("test").await {
            //     Ok(_) => println!("Message sent successfully"),
            //     Err(err) => eprintln!("Failed to send Telegram message: {:?}", err),
            // }
        });
        // if let Err(err) = tgclient.send_telegram_message("test").await {
        //     eprintln!("Failed to send Telegram message: {:?}", err);
        // }
        Ok(())
    }
}