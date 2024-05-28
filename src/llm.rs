use openai_dive::v1::api::Client;
use openai_dive::v1::models::Gpt4Engine;
use openai_dive::v1::resources::chat::{ChatCompletionParameters, ChatMessage, Role,ChatMessageContent};
// use reqwest::Client;

pub struct AIClient{
    ai_client: Client
}

impl AIClient{
    pub fn new(api_key:&str) -> Self{
        // let api_key = api_key;
        // let config = OpenAIConfig::new()
        //     .with_api_base("https://api.deepseek.com/chat")
        //     .with_api_key(api_key)
        //     .with_org_id("the-continental");
        

        let http_client= reqwest::Client::builder().build().unwrap();

        let client = Client {
            http_client: http_client,
            base_url: "https://api.deepseek.com/v1".to_string(),
            api_key: String::from(api_key),
            organization: None,
            project: None
        };



        AIClient{ai_client:client}
    }


    /// 利用大模型总结目标内容
    pub async fn summarize(&self, content:&str)->Result<String,()>{
        // let answer;

        let parameters = ChatCompletionParameters {
            model: "deepseek-chat".to_string(),
            messages: vec![
                ChatMessage {
                    role: Role::User,
                    content: ChatMessageContent::Text(format!("帮我用中文总结下面的内容，最大不超过2000字: \n{}",content)),
                    ..Default::default()
                },
            ],
            // max_tokens: Some(12),
            ..Default::default()
        };
    
        let result = self.ai_client.chat().create(parameters).await.unwrap();
    
        // println!("{:#?}", result);

        let summary=match &result.choices.first().unwrap().message.content{
            ChatMessageContent::Text(text) => {
                text.clone()
            },
            _ => String::from("大模型返回异常")
        };
        

        Ok(summary)
    }

}



#[cfg(test)]
mod tests{
    use std::error::Error;

    use super::*;
    use crate::config::Config;
    use crate::models::*;
    use crate::tgclient::*;
    use crate::v2ex_client::*;
    use crate::llm::AIClient;
    use tokio;
    use reqwest::Client;
    use tokio::time::Duration;
    use teloxide::prelude::*;
    use chrono::prelude::*;
    use chrono::Duration as ChronoDuration;
    use clap::Parser;
    use tokio::runtime::Runtime;
    use std::fs::File;
    use std::io::Write;



    #[test]
    fn test_llm() -> Result<(), Box<dyn Error>>{
        let config = Config::from_file("config.toml");
        let mut base_date = Utc::now().format("%Y%m%d").to_string();
        // let bot = Bot::new(&config.telegram.api_token);
        // let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));
        let ai_client=&AIClient::new(&config.deepseek.api_token);


        let new_runtime = Runtime::new()?;
        // 同步执行
        new_runtime.block_on(async {

            let origin_news_content="
            西风酒旗市，细雨菊花天。
            ";
            let summary=ai_client.summarize(origin_news_content).await.unwrap();
            println!("{}", &summary);

        });
        Ok(())
    }
}
