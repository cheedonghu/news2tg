use std::error::Error;
use reqwest::Client;
use async_trait::async_trait;
use crate::traits::monitor::Monitor;



// 定义 MonitorHackerNewsError
#[derive(Debug)]
pub enum MonitorHackerNewsError {
    NetworkError(String),
    ParseError(String),
}
impl Error for MonitorHackerNewsError {}
impl std::fmt::Display for MonitorHackerNewsError {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        match &self {
            MonitorHackerNewsError::NetworkError(msg) => write!(f, "Network Error: {}", msg),
            MonitorHackerNewsError::ParseError(msg) => write!(f, "Parse Error: {}", msg),
        }
    }
}

// 定义 MonitorHackerNews 结构体
pub struct MonitorHackerNews{
    client: Client
}

// 实现 Monitor trait for MonitorHackerNews
#[async_trait]
impl Monitor for MonitorHackerNews {
    type Output = Vec<String>;
    type MonitorError = MonitorHackerNewsError;

    async fn fetch_hot(&self) -> Result<Self::Output, Self::MonitorError> {
        let url = "https://hacker-news.firebaseio.com/v0/topstories.json?print=pretty";

        let result = match self.client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
            Ok(resp)=> match resp.json::<Vec<u64>>().await{
                Ok(json)=>{
                    let string_array: Vec<String> =json.iter().map(|&i| i.to_string()).collect();
                    string_array
                },
                Err(err) => {
                    eprintln!("Parse HackerNews's hot content response to json failed: {:?}", err);
                    return Err(MonitorHackerNewsError::ParseError("Parse HackerNews's response to json failed".to_string()))
                }
            },
            Err(err)=>{
                eprintln!("Fetch HackerNews's hot content response failed: {:?}", err);
                return Err(MonitorHackerNewsError::NetworkError("Fetch HackerNews's response failed".to_string()))
            }
        };
        
        Ok(result)
    }

    async fn fetch_new(&self) -> Result<Self::Output, Self::MonitorError> {
        let url = "https://hacker-news.firebaseio.com/v0/newstories.json?print=pretty";

        let result = match self.client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
            Ok(resp)=> match resp.json::<Vec<u64>>().await{
                Ok(json)=>{
                    let string_array: Vec<String> =json.iter().map(|&i| i.to_string()).collect();
                    string_array
                },
                Err(err) => {
                    eprintln!("Parse HackerNews's newest content response to json failed: {:?}", err);
                    return Err(MonitorHackerNewsError::ParseError("Parse HackerNews's response to json failed".to_string()))
                }
            },
            Err(err)=>{
                eprintln!("Fetch HackerNews's newest content response failed: {:?}", err);
                return Err(MonitorHackerNewsError::NetworkError("Fetch HackerNews's response failed".to_string()))
            }
        };
        
        Ok(result)
    }
}

impl MonitorHackerNews {
    pub fn new(client: Client) -> Self{
        MonitorHackerNews{client}
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
        // let mut HackerNews_client=HackerNewsClient::new(Client::new(), base_date.clone());
        let config = Config::from_file("myconfig.toml");
        let client=Client::new();
        
        let monitor=MonitorHackerNews::new(client);
        let result=monitor.fetch_hot().await.map_err(|err| eprintln!("error: {:?}", err)).unwrap();

        println!("result is :{:?}", result.get(0))

    }
}





