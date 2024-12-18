use async_trait::async_trait;
use openai_dive::v1::api::Client;
use openai_dive::v1::resources::chat::{ChatCompletionParameters, ChatMessage, Role,ChatMessageContent};
use chrono::Local;

use crate::common::models::News2tgError;
use crate::traits::ai_helper::AIHelper;

pub struct AIHelperDeepSeek{
    ai_client: Client
}

impl AIHelperDeepSeek {
    pub fn new(api_key: String) -> Self{
        let http_client= reqwest::Client::builder().build().unwrap();

        let ai_client = Client {
            http_client: http_client,
            base_url: "https://api.deepseek.com/v1".to_string(),
            api_key: String::from(api_key),
            organization: None,
            project: None
        };

        AIHelperDeepSeek{ai_client}
    }
}

#[async_trait]
impl AIHelper for AIHelperDeepSeek {
    type Output = String;

    async fn summarize(&self, content: String) -> Result<Self::Output, News2tgError> {
        println!("{} 利用大模型将内容转为中文", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
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

        let result: String = match llm_response {
            Ok(result_str)=>{
                match &result_str.choices.first().unwrap().message.content{
                    ChatMessageContent::Text(text) => {
                        text.clone()
                    },
                    _ => String::from("大模型返回非String内容")
                }
            },
            _ =>{
                String::from("大模型返回异常")
            }
        };

        Ok(result)
    }


    async fn translate(&self) -> Result<Self::Output, News2tgError> {
        Err(News2tgError::NotifyError("无需实现".to_string()))
    }
}



