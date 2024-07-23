use openai_dive::v1::api::Client;
use openai_dive::v1::endpoints::chat::Chat;
use openai_dive::v1::models::Gpt4Engine;
use openai_dive::v1::resources::chat::{ChatCompletionParameters, ChatMessage, Role,ChatMessageContent};
// use reqwest::Client;
use futures::StreamExt;

pub struct AIClient{
    ai_client: Client
}

impl AIClient{
    pub fn new(api_key:&str) -> Self{

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
    /// 参考：https://blog.betacat.io/post/2023/06/summarize-hacker-news-by-chatgpt/
    pub async fn summarize(&self, content:&str)->Result<String,()>{
        // 字符数超过3w就不用调用大模型总结了，上下文不够
        if content.len()>30000 {
            let result=String::from(format!("字符数为{}，超过32k的上下文窗口，等待api支持embedding功能",content.len()));
            return Ok(result)
        }

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
    
        let llm_response = self.ai_client.chat().create(parameters).await;

        if cfg!(debug_assertions) {
            println!("{:#?}", llm_response);
        }

        let result;
        match llm_response {
            Ok(result_str)=>{
                match &result_str.choices.first().unwrap().message.content{
                    ChatMessageContent::Text(text) => {
                        result=text.clone()
                    },
                    _ => result=String::from("大模型返回非String内容")
                };
            }
            _ =>{
                result=String::from("大模型返回异常")
            }
        }

        Ok(result)
    }

}



#[cfg(test)]
mod tests{
    use std::error::Error;

    use super::*;
    use crate::config::Config;

    use crate::llm::AIClient;
    use tokio;
    use tokio::runtime::Runtime;
    use std::fs::File;
    use std::io::Read;



    #[test]
    fn test_llm() -> Result<(), Box<dyn Error>>{
        let config = Config::from_file("config.toml");
        // let mut base_date = Utc::now().format("%Y%m%d").to_string();
        // let bot = Bot::new(&config.telegram.api_token);
        // let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));
        let ai_client=&AIClient::new(&config.deepseek.api_token);


        let new_runtime = Runtime::new()?;
        // 同步执行
        new_runtime.block_on(async {

            // let origin_news_content="
            // 西风酒旗市，细雨菊花天。
            // ";

            let mut file = File::open("./output.html").unwrap();
            let mut origin_news_content: String=String::new();
            file.read_to_string(&mut origin_news_content).unwrap();
    // 

            let summary=ai_client.summarize(&origin_news_content).await.unwrap();
            println!("{}", &summary);

        });
        Ok(())
    }
}
