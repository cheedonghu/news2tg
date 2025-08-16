use std::collections::HashMap;
use std::error::Error;
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Local};
use reqwest::Client;
use tokio::time::interval;

use crate::ChronoDuration;
use crate::common::config::Config;
use crate::common::models::{News2tgError, News2tgNotifyBase, Topic};
use crate::common::tools;
use crate::tokio::sync::RwLock;
use crate::traits::ai_helper::AIHelper;
use crate::traits::monitor::Monitor;
use crate::traits::news2tg::News2tg;
use crate::traits::notify::Notify;

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
pub struct MonitorV2EX<N: Notify> {
    http_client: Client,
    // map里面一个是url用来去重，一个是日期用来清理内存占用
    pushed_urls: RwLock<HashMap<String, String>>,
    notify_client: N,
}


impl<N: Notify> MonitorV2EX<N> {
    pub fn new(http_client: Client, notify_client: N) -> Self{
        MonitorV2EX{
            http_client: http_client,
            pushed_urls: RwLock::new(HashMap::new()),
            notify_client,
        }
    }

    pub fn get_pushed_urls(&mut self) -> &mut RwLock<HashMap<String, String>>{
        &mut self.pushed_urls
    }

    async fn clean_old_urls(&mut self, now: DateTime<Local>){
        let mut v2ex_cutoff_date = format!("{}",
        now.checked_sub_signed(ChronoDuration::days(5)).unwrap().format("%Y%m%d"));
    
        self.pushed_urls.write().await.retain(|_, date_str| {
            date_str >= &mut v2ex_cutoff_date
        });
    }
}

// 实现 Monitor trait for MonitorV2EX
#[async_trait]
impl<N: Notify+ Send + Sync> Monitor for MonitorV2EX<N> {
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
impl<N: Notify+ Send + Sync> News2tg for MonitorV2EX<N> {
    type Param = ();
    type Output = Vec<News2tgNotifyBase>;

    /// 按配置文件中的规则调用monitor接口获取需要的内容
    async fn fetch(&mut self, config: &Config) -> Result<Self::Output, News2tgError>{
        let mut result:Vec<News2tgNotifyBase>=Vec::new();
        let mut hot_topics: Vec<Topic>=Vec::new();
        let mut new_topics: Vec<Topic>=Vec::new();
        if config.features.v2ex_fetch_hot{
            hot_topics=self.fetch_hot().await.unwrap().into();
        }
        if config.features.v2ex_fetch_latest{
            new_topics=self.fetch_new().await.unwrap().into();
        }
        // 获取的帖子
        // let topics: Vec<Topic>=hot_topics.into_iter()
        // .chain(new_topics.into_iter())
        // .collect();

        let hot_title="热帖推送";
        let new_title="新帖推送";
        let current_date=Local::now().format("%Y%m%d").to_string();

        // 判断是否有目标帖子
        for topic in hot_topics {
            if !self.get_pushed_urls().read().await.contains_key(&topic.url) {
                let mut output=News2tgNotifyBase::default();

                let title=tools::truncate_utf8(&topic.title, 4000);
                let content_title=tools::escape_markdown_v2(&title);
                // message.push_str(&format!("*{}*: [{}]({})\n",section_title, topic.title, topic.url));
                output.set_title(title);
                output.set_content(format!("*{}*: [{}]({})\n",hot_title, content_title, &topic.url));
                output.set_url((&topic.url).to_string());
                result.push(output);
                self.get_pushed_urls().write().await.insert(topic.url.clone(), current_date.to_string());
            }
        }

        for topic in new_topics {
            if !self.get_pushed_urls().read().await.contains_key(&topic.url) {
                let mut output=News2tgNotifyBase::default();

                let title=tools::truncate_utf8(&topic.title, 4000);
                if !config.features.v2ex_fetch_latest_keyword.is_empty()
                    && !config.features.v2ex_fetch_latest_keyword.iter().any(|keyword| title.contains(keyword)) {
                    // 当有关键字过滤，却匹配不到关键字时，直接跳过此条记录
                    // eprintln!("当前标题:{}未包含关键字；跳过", title);
                    continue;
                }
                let content_title=tools::escape_markdown_v2(&title);
                output.set_title(title);
                output.set_content(format!("*{}*: [{}]({})\n",new_title, content_title, &topic.url));
                output.set_url((&topic.url).to_string());
                result.push(output);
                self.get_pushed_urls().write().await.insert(topic.url.clone(), current_date.to_string());
            }
        }

        Ok(result)
    }

    async fn ai_transfer(&mut self, _param: Self::Output) -> Result<Self::Output, News2tgError>{
        // Implementation here
        Err(News2tgError::MonitorError("v2ex监控无需ai总结".to_string()))
    }

    async fn notify(&mut self, param: Self::Output) -> Result<bool, News2tgError>{
        // let content:&Vec<News2tgNotifyBase> = param;
        // Implementation here
        
        let contents:Vec<String>=param.iter().map(|item| item.content().clone()).collect();

        let _ = self.notify_client.notify_batch(&contents).await;

        Ok(true)
    }

    /// 这里决定该监控类用哪个ai和推送到哪
    async fn run(&mut self, config: &Config) -> Result<(), News2tgError> {
        // 创建一个 2min 的周期定时器，可自行调整
        let mut main_ticker = interval(Duration::from_secs(60*2));

        loop {
            main_ticker.tick().await;

            // 核心逻辑
            let result: Vec<News2tgNotifyBase>=match self.fetch(config).await {
                Ok(output)=> output,
                Err(err)=> {
                    eprintln!("获取V2EX信息失败");
                    return Err(err);
                }
            };
    
            if result.capacity()>0{
                let _ =self.notify(result).await;
            }
    
            self.clean_old_urls(Local::now()).await;
        }
    }
    
}



#[cfg(test)]
mod tests{
    use crate::{common::config::Config, implementations::notify_tg::NotifyTelegram};

    use super::*;

    #[tokio::test]
    async fn test_fetch(){
        // let mut base_date = Utc::now().format("%Y%m%d").to_string();
        // let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
        let config = &Config::from_file("myconfig.toml");
        let client=Client::new();

        let tg_client=NotifyTelegram::new(config.telegram.api_token.to_string(), config.telegram.chat_id.parse::<i64>().expect("Invalid Tg chat id"));
        let mut monitor=MonitorV2EX::new(client,tg_client );
        // let result=monitor.fetch_hot().await.map_err(|err| eprintln!("error: {:?}", err)).unwrap();

        match monitor.fetch_hot().await{
            Ok(result)=>{
                println!("result is :{:?}", result.get(0));
            },
            Err(err)=>{
                println!("err:{}", err);
            }
        };
        // println!("result is :{:?}", result.get(0))
    }
   
    #[tokio::test]
    async fn test_run(){
        // let mut base_date = Utc::now().format("%Y%m%d").to_string();
        // let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
        let config = &Config::from_file("myconfig.toml");
        let client=Client::new();

        let tg_client=NotifyTelegram::new(config.telegram.api_token.to_string(), config.telegram.chat_id.parse::<i64>().expect("Invalid Tg chat id"));
        let mut monitor=MonitorV2EX::new(client,tg_client );
        // let result=monitor.fetch_hot().await.map_err(|err| eprintln!("error: {:?}", err)).unwrap();

        let _ = monitor.run(&config).await;
        // println!("result is :{:?}", result.get(0))
    }

}





