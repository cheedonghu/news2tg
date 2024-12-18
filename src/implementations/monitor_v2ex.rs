use std::error::Error;
use reqwest::Client;
use async_trait::async_trait;
use crate::tokio::sync::RwLock;
use std::collections::HashMap;
use crate::traits::monitor::Monitor;

use crate::common::models::{News2tgError, Topic,News2tgNotifyBase};
use crate::traits::news2tg::News2tg;
use crate::common::config::Config;

use super::ai_helper_deepseek::AIHelperDeepSeek;
use crate::traits::ai_helper::AIHelper;
use crate::traits::notify::{self, Notify};


// 定义 MonitorV2EXError
#[derive(Debug)]
pub enum MonitorV2EXError {
    NetworkError(String),
    ParseError(String),
}
impl Error for MonitorV2EXError {}
impl std::fmt::Display for MonitorV2EXError {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        match &self {
            MonitorV2EXError::NetworkError(msg) => write!(f, "Network Error: {}", msg),
            MonitorV2EXError::ParseError(msg) => write!(f, "Parse Error: {}", msg),
        }
    }
}

// 定义 MonitorV2EX 结构体
pub struct MonitorV2EX{
    base_info: News2tgNotifyBase,
    http_client: Client,
    pushed_urls: RwLock<HashMap<String, String>>
}


impl MonitorV2EX {
    pub fn new(http_client: Client) -> Self{
        MonitorV2EX{
            base_info: News2tgNotifyBase::default(),
            http_client: http_client,
            pushed_urls: RwLock::new(HashMap::new())
        }
    }
}

// 实现 Monitor trait for MonitorV2EX
#[async_trait]
impl Monitor for MonitorV2EX {
    type Output = Vec<Topic>;
    type MonitorError = MonitorV2EXError;

    async fn fetch_hot(&self) -> Result<Self::Output, Self::MonitorError> {
        // 这里可以实现实际的网络请求逻辑
        let url = "https://www.v2ex.com/api/topics/hot.json";

        let result = match self.http_client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
            Ok(resp)=> match resp.json::<Vec<Topic>>().await{
                Ok(json)=>{
                    json
                },
                Err(err) => {
                    eprintln!("Parse V2EX's hot content response to json failed: {:?}", err);
                    return Err(MonitorV2EXError::ParseError("Parse V2EX's response to json failed".to_string()))
                }
            },
            Err(err)=>{
                eprintln!("Fetch V2EX's hot content response failed: {:?}", err);
                return Err(MonitorV2EXError::NetworkError("Fetch V2EX's response failed".to_string()))
            }
        };
        
        Ok(result)
    }

    async fn fetch_new(&self) -> Result<Self::Output, Self::MonitorError> {
        let url = "https://www.v2ex.com/api/topics/latest.json";

        let result = match self.http_client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
            Ok(resp)=> match resp.json::<Vec<Topic>>().await{
                Ok(json)=>{
                    json
                },
                Err(err) => {
                    eprintln!("Parse V2EX's latest content to json failed: {:?}", err);
                    return Err(MonitorV2EXError::ParseError("Parse V2EX's latest content to json failed".to_string()))
                }
            },
            Err(err)=>{
                eprintln!("Fetch V2EX's latest content failed: {:?}", err);
                return Err(MonitorV2EXError::NetworkError("Fetch V2EX's response failed".to_string()))
            }
        };
        
        Ok(result)
    }
}

#[async_trait]
impl News2tg for MonitorV2EX {
    type Param = ();
    type Output = Vec<Topic>;

    fn get_base(&mut self) -> &mut News2tgNotifyBase {
        // Implementation here
        &mut self.base_info
    }



    /// 按配置文件中的规则调用monitor接口获取需要的内容
    async fn fetch(&self, config: &Config) -> Result<Self::Output, News2tgError>{
        if(config.features.v2ex_fetch_hot){
            //获取热帖
            let topics: Vec<Topic>=self.fetch_hot().unwrap().into();

            // 判断是否有目标帖子
            for topic in topics {
                if !shared_item.v2ex_pushed_urls.read().await.contains_key(&topic.url) {
                    let title=truncate_utf8(&topic.title, 4000);
                    let title=escape_markdown_v2(&title);
                    // message.push_str(&format!("*{}*: [{}]({})\n",section_title, topic.title, topic.url));
                    new_topic_message.push(format!("*{}*: [{}]({})\n",section_title, title, topic.url));
                    shared_item.v2ex_pushed_urls.write().await.insert(topic.url.clone(), current_date.to_string());
                }
            }
        }

        // 判断是否已经推送

        // 获取内容格式化后返回

    }

    async fn ai_transfer<T>(&self, param: &T) -> Result<Self::Output, News2tgError>
    where
        T: AIHelper + Send + Sync,
    {
        // Implementation here
    }

    async fn notify<T>(&self, param: &T) -> Result<Self::Output, News2tgError>
    where
        T: Notify + Send + Sync,
    {
        // Implementation here
    }

    /// 这里决定该监控类用哪个ai和推送到哪
    async fn run(&self, config: &Config) -> Result<Self::Output, News2tgError> {
        // Implementation here
    }
}



#[cfg(test)]
mod tests{

    use super::*;
    use chrono::Utc;
    use crate::config::Config;

   
    #[tokio::test]
    async fn test_url(){
        // let mut base_date = Utc::now().format("%Y%m%d").to_string();
        // let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
        let config = Config::from_file("myconfig.toml");
        let client=Client::new();
        
        let monitor=MonitorV2EX::new(client);
        let result=monitor.fetch_hot().await.map_err(|err| eprintln!("error: {:?}", err)).unwrap();

        println!("result is :{:?}", result.get(0))

    }
                       // 返回内容
}





