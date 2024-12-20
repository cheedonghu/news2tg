use async_trait::async_trait;
use teloxide::prelude::*;
use teloxide::types::ParseMode;
use tokio::time::{Duration, sleep};

use crate::traits::notify::Notify;
use crate::common::models::News2tgError;

pub struct NotifyTelegram{
    bot: Bot,
    chat_id: ChatId
}


impl NotifyTelegram {
    pub fn new(bot_token: String, chat_id: i64) -> Self{

        let bot = Bot::new(bot_token);
        let chat_id = ChatId(chat_id);

        NotifyTelegram{bot, chat_id}
    }
}

#[async_trait]
impl Notify for NotifyTelegram{
    type Output = bool;
    type NotifyError = News2tgError;

    async fn notify(&self, content: &String) -> Result<Self::Output, Self::NotifyError>{
        // println!("{}",content);
        // write_content_to_file("【发送内容】", content, false)?;

        // 不显示预览： 1.网页元数据问题 2.频率太快？
        let _result=self.bot.send_message(self.chat_id, content)
            .parse_mode(ParseMode::MarkdownV2).disable_web_page_preview(false)
            .send()
            .await
            .map_err(|err| {
                eprintln!("telegram推送失败：: {:?}", err);
                News2tgError::NotifyError("telegram推送失败".to_string())
            });

        Ok(true)
    }

    async fn notify_batch(&self, contents: &Vec<String>) -> Result<Self::Output, Self::NotifyError>{

        // 这里循环发可能触发tg限制 todo
        for content in contents{
            println!("{}",content);
            // 不显示预览： 1.网页元数据问题 2.频率太快？
            let _result=self.bot.send_message(self.chat_id, content)
            .parse_mode(ParseMode::MarkdownV2).disable_web_page_preview(false)
            .send()
            .await
            .map_err(|err| {
                eprintln!("telegram推送失败：: {:?}", err);
                News2tgError::NotifyError("telegram推送失败".to_string())
            });
            // 停顿1.5s防止预览不显示
            sleep(Duration::from_millis(1500)).await;
        }

        Ok(true)
    }
}

#[cfg(test)]
mod tests{

    use super::*;
    use crate::config::Config;

    #[tokio::test]
    async fn test_send_telegram_message(){
        let config = Config::from_file("myconfig.toml");
        let tgclient=NotifyTelegram
        ::new(config.telegram.api_token, config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));

        let msg=String::from(
            r"*Hacker News Top推送*: 
            Comment Site: https://news\.ycombinator\.com/item?id\=40530365
            AI总结: 待定
           [源内容: ](https://vickiboykis.com/2024/05/20/dont-worry-about-llms/)");

        tgclient.notify(&msg).await.unwrap();
    }
                            // 返回内容
}