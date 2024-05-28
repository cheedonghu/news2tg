use async_openai::{Client, config::OpenAIConfig,types::CreateCompletionRequestArgs};


pub struct AIClient{
    ai_client: Client<OpenAIConfig>
}

impl AIClient{
    pub fn new(api_key:&str) -> Self{
        let api_key = api_key;
        let config = OpenAIConfig::new()
            .with_api_base("https://api.deepseek.com/v1")
            .with_api_key(api_key)
            .with_org_id("the-continental");
        
        let client = Client::with_config(config);
        AIClient{ai_client:client}
    }


    /// 利用大模型总结目标内容
    pub async fn summarize(&self, content:&str)->Result<String,()>{
        let answer;

        // 发送给模型
        // Create request using builder pattern
        // Every request struct has companion builder struct with same name + Args suffix
        let request = CreateCompletionRequestArgs::default()
        .model("deepseek-chat")
        .prompt(&format!("帮我用中文总结下面的内容，最大不超过2000字: \n{}",content))
        // .max_tokens(40_u16)
        .build()
        .unwrap();

        // Call API
        let response = self.ai_client
        .completions()      // Get the API "group" (completions, images, etc.) from the client
        .create(request)    // Make the API call in that "group"
        .await;

        match response {
            Ok(res) => {
                println!("Response: {:?}", res);
                // 继续处理 res
                answer=String::from(&res.choices.first().unwrap().text);
            }
            Err(e) => {
                println!("API call failed: {:?}", e);
                answer=String::from("大模型交互失败")
            }
        }

        Ok(answer)
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
    fn test() -> Result<(), Box<dyn Error>>{
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
