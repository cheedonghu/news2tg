use std::error::Error;
use reqwest::{Client};
use async_trait::async_trait;
use crate::traits::monitor::Monitor;

use crate::models::Topic;


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
    client: Client
}

// 实现 Monitor trait for MonitorV2EX
#[async_trait]
impl Monitor for MonitorV2EX {
    type Output = Vec<Topic>;
    type MonitorError = MonitorV2EXError;

    async fn fetch_hot(&self) -> Result<Self::Output, Self::MonitorError> {
        // 这里可以实现实际的网络请求逻辑
        let url = "https://www.v2ex.com/api/topics/hot.json";

        let result = match self.client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
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

        let result = match self.client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
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

impl MonitorV2EX {
    pub fn new(client: Client) -> Self{
        MonitorV2EX{client}
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





