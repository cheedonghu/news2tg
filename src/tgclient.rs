use teloxide::prelude::*;
use teloxide::types::ParseMode;
use crate::tools::truncate_utf8;

pub struct TgClient {
    pub bot: Bot,
    pub chat_id: ChatId,
}

impl TgClient {
    pub fn new(bot: Bot,chat_id: ChatId)-> Self{
        TgClient { bot, chat_id}
    }

    pub async fn send_telegram_message(&self, text: String) -> Result<(), teloxide::RequestError> {
        let truncated_text=truncate_utf8(&text, 4096);
        self.bot.send_message(self.chat_id, truncated_text)
            .parse_mode(ParseMode::MarkdownV2)
            .send()
            .await?;
        Ok(())
    }


}

// pub async fn send_telegram_message(bot: &Bot, chat_id: ChatId, text: String) -> Result<(), teloxide::RequestError> {
//     bot.send_message(chat_id, text)
//         .parse_mode(ParseMode::MarkdownV2)
//         .send()
//         .await?;
//     Ok(())
// }