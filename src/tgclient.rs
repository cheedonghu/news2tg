use teloxide::prelude::*;
use teloxide::types::ParseMode;
use tokio::runtime::Runtime;
use std::error::Error;

use crate::tools::truncate_utf8;


pub struct TgClient {
    pub bot: Bot,
    pub chat_id: ChatId,
}

impl TgClient {
    pub fn new(bot: Bot,chat_id: ChatId)-> Self{
        TgClient { bot, chat_id}
    }

    pub async fn send_telegram_message(&self, text: &str) -> Result<(), teloxide::RequestError> {
        let truncated_text=truncate_utf8(text, 4096);
        self.bot.send_message(self.chat_id, truncated_text)
            .parse_mode(ParseMode::MarkdownV2)
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
            let mut message: String=String::new();
            message.push_str(&format!("*热帖推送*: [{}]({})\n", "测试", "luyublog.com"));
            // message.push_str(&format!("*主 题*: [{}]({})\n", topic.title, topic.url));
            tgclient.send_telegram_message(&message).await.unwrap();
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