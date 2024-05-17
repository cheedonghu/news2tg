use teloxide::prelude::*;
use teloxide::types::ParseMode;


pub async fn send_telegram_message(bot: Bot, chat_id: ChatId, text: String) -> Result<(), teloxide::RequestError> {
    bot.send_message(chat_id, text)
        .parse_mode(ParseMode::MarkdownV2)
        .send()
        .await?;
    Ok(())
}