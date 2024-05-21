use teloxide::prelude::*;
use teloxide::types::ParseMode;
use tokio::runtime::Runtime;
use std::error::Error;

use crate::tools::*;


pub struct TgClient {
    pub bot: Bot,
    pub chat_id: ChatId,
}

impl TgClient {
    pub fn new(bot: Bot,chat_id: ChatId)-> Self{
        TgClient { bot, chat_id}
    }

    pub async fn send_message(&self, text: &str) -> Result<(), teloxide::RequestError> {
        let truncated_text=truncate_utf8(text, 4096);
        // 转义特殊字符
        let escaped_text=escape_markdown_v2(&truncated_text);
        self.bot.send_message(self.chat_id, escaped_text)
            .parse_mode(ParseMode::MarkdownV2)
            .send()
            .await?;
        Ok(())
    }

    /// 分批次发送消息
    pub async fn send_batch_message(&self, message_array: &Vec<String>) -> Result<(), teloxide::RequestError> {
        // 这里循环发可能触发tg限制 todo
        for message in message_array{
            self.bot.send_message(self.chat_id, message)
                .parse_mode(ParseMode::MarkdownV2)
                .send()
                .await?;
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
            let title=String::from("测试title.-");
            let mut message_arrary = Vec::new();
            message_arrary.push(format!("*热帖推送*: [{}]({})\n", title, "luyublog.com"));
            message_arrary.push(format!("*热帖推送*: [{}]({})\n", title, "luyublog.com"));
            
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