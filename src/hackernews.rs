use futures::TryFutureExt;
use reqwest::{Client};
use std::error::Error;
use log::{info, error};
use crate::llm::AIClient;
use crate::models::SharedItem;
use crate::tgclient::{self, TgClient};
use std::collections::HashMap;
use scraper::{Html,Selector};
use tokio::sync::RwLock;
use toml::Value::Integer;
use chrono::prelude::{NaiveDate,NaiveDateTime};
use chrono::Duration as ChronoDuration;
use std::cmp::Ordering;
use news2tg::myservice::my_service_client::MyServiceClient;
use news2tg::myservice::ServiceRequest;
use tonic::transport::Channel;
use tonic::Status;
use chrono::{Datelike, Duration, Local, TimeZone, Timelike};


use crate::tools;
use crate::models::MyError;

pub struct HackerNews{
    pub http_client: Client,
    pub current_date: String,
    pub top_num: usize,
    // 推送的帖子要距今多久(单位: H)
    pub time_gap: usize
}

impl HackerNews{

    pub fn new(http_client: Client, 
        current_date: String, 
        top_num: usize, 
        time_gap:usize) -> Self{
        HackerNews{http_client, 
            current_date, 
            top_num,
            time_gap}
    }

    /// 更新当前时间
    pub fn update_current_date(&mut self, new_date: &str){
        self.current_date=String::from(new_date);
    }

    
    /// 从网页里面判断发帖时间
    fn judge_news_date(&self, response:&str, time_gap:usize) -> bool{
        println!("{} 开始判断帖子日期是否在范围内", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
        // Parse the HTML
        let document = Html::parse_document(response);
        let selector = Selector::parse("tbody tr td.subtext span.age").unwrap();

        // document.select(&selector)
        let mut title_time:Option<String>=None;
        for element in document.select(&selector) {
            let text = element.text().collect::<Vec<_>>().join(" ");
            // if cfg!(debug_assertions) {
            //     println!("{}", &text);
            // }

            //单位是hour，数字要大于指定数字
            let text_vec: Vec<&str>=text.split(' ').collect();
            if text_vec.len()==3 && text_vec.get(1).expect("单位获取失败").eq_ignore_ascii_case("hours") && text_vec.get(0).unwrap().parse::<usize>().expect("数字部分转换失败")>time_gap{
                return true
            }
            title_time=Some(text);
        }

        println!("{}",format!("帖子日期: {} 不符合推送要求",title_time.unwrap_or("帖子日期获取失败".to_string())));
        false
    }

    
    /// 从hacker news的comment页面中提取出源网址： 在titleline的href内
    pub async fn get_news_origin_url(&self, response:&str) -> Result<String,Box<dyn Error>>{
        println!("{} 开始解析源网址", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
        let mut news_url: String=String::new();

        // let response=self.client.get(url).send().await?.text().await?;
        // Parse the HTML
        let document = Html::parse_document(response);
        let selector = Selector::parse("span.titleline a").unwrap();

        // Extract the link
        if let Some(element) = document.select(&selector).next() {
            if let Some(href) = element.value().attr("href") {
                println!("识别到的源网址为: {}", href);
                // 要保证是http格式
                if href.starts_with("http") || href.starts_with("https"){
                    news_url=String::from(href);
                }else{
                    println!("识别到的源网址格式异常");
                }
            } else {
                println!("未找到源网址");
            }
        } else {
            println!("span.titleline a 没找到对应内容");
        }
        
        Ok(news_url)
    }
    

    /// 调用hacker news的top分类接口
    async fn get_hacker_news_top_info(&self) -> Result<Vec<String>,Box<dyn Error>>{
        let url = "https://hacker-news.firebaseio.com/v0/topstories.json?print=pretty";
        let response = self.http_client.get(url).send().await?.json::<Vec<u64>>().await?;

        let string_array: Vec<String> = response.iter()
            .map(|&i| i.to_string())
            .collect();

        Ok(string_array)
    }

    /// 调用hacker news的newest分类接口
    async fn get_hacker_news_new_info(&self) -> Result<Vec<String>,Box<dyn Error>>{
        let url = "https://hacker-news.firebaseio.com/v0/newstories.json?print=pretty";
        let response = self.http_client.get(url).send().await?.json::<Vec<u64>>().await?;

        let string_array: Vec<String> = response.iter()
            .map(|&i| i.to_string())
            .collect();

        Ok(string_array)
    }

    /// 调用hacker news的best分类接口
    async fn get_hacker_news_best_info(&self) -> Result<Vec<String>,Box<dyn Error>>{
        let url = "https://hacker-news.firebaseio.com/v0/beststories.json?print=pretty";
        let response = self.http_client.get(url).send().await?.json::<Vec<u64>>().await?;

        let string_array: Vec<String> = response.iter()
            .map(|&i| i.to_string())
            .collect();

        Ok(string_array)
    }


    /// 调用gRPC，从pyhton获取网页摘要
    async fn get_digest_from_python(&mut self, origin_news_url: &str,rpc_client: &mut MyServiceClient<Channel> )-> Result<String, Box<dyn Error>>{
        println!("{} 开始调用gRPC接口获取源网址摘要", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
        // 创建请求
        let request = tonic::Request::new(ServiceRequest {
            input: origin_news_url.to_string(),
        });

        // 调用远程函数
        match rpc_client.remote_function(request).await {
            Ok(response) => {
                let digest=response.into_inner().output;
                // 打印服务器响应
                // info!("调用gRPC结果：\"{:?}\"", digest);
                println!("{} 调用gRPC结果：\"{:?}\"", Local::now().format("%Y年%m月%d日 %H:%M:%S"),digest);
                // 返回结果
                Ok(digest)
            },
            Err(e) => {
                // error!("调用gRPC失败: {:?}", e);
                println!("{} 调用gRPC失败", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
                Err(Box::new(e))
            },
        }
    }

    /// 获取目标帖子，以及其相关信息
    pub async fn process_top_news(&mut self,
        shared_item: &mut SharedItem,
        rpc_client: &mut MyServiceClient<Channel>,
        llm_client: &AIClient, 
        tg_client: &TgClient,
        ) -> Result<(),Box<dyn Error>>{
        // 最新id数组
        let id_array:Vec<String>;
        match self.get_hacker_news_top_info().await {
            Ok(res)=>id_array=res,
            _=>return Ok(())
        }
        // 拿出前n的id
        let truncated_id: Vec<String>=id_array.iter().take(self.top_num.clone()).cloned().collect();

        // 将新出现的进行特性过滤
        let current_date=self.current_date.to_string();
        let pushed_urls: &mut RwLock<HashMap<String,String>>=&mut shared_item.hackernews_pushed_urls;

        for id in truncated_id{
            if pushed_urls.read().await.contains_key(&id){
                // 已推送的不处理
                println!("当前id:{} 已推送", id);
                continue;
            }
            println!("{} 开始解析id: {}", Local::now().format("%Y年%m月%d日 %H:%M:%S"), &id);
            // 新出现的
            let url=format!("https://news.ycombinator.com/item?id={}",&id);
            let response=self.http_client.get(url.clone()).send().await?.text().await?;

            // 仅创建时间不算短的才继续解析推送否则推送频率太高
            if !self.judge_news_date(&response, self.time_gap) {
                // 不满足要求，当前url跳过
                continue;
            }

            // ai总结：1. 获取源信息url 2.获取url链接内容 3.发送给大模型进行总结
            let origin_news_url=self.get_news_origin_url(&response).await.unwrap();

            // 从python那边获取网页摘要;
            match self.get_digest_from_python(&origin_news_url, rpc_client).await {
                Ok(digest) => {
                    // 成功拿到摘要, 交给大模型总结
                    let summary=llm_client.summarize(&digest).await.unwrap_or(String::from("总结失败"));
                    // 格式化消息
                    let text = format!("*Hacker News Top推送*: \n Comment Site:{}\n\n {}\n\n[{}]({})\n", 
                                        tools::escape_markdown_v2(&url), 
                                        format!("AI总结: {}", tools::escape_markdown_v2(&summary)),
                                        "源内容网页: ", tools::escape_markdown_v2(&origin_news_url));
                    // 推送
                    tg_client.send_message(&text).await?;
                },
                Err(e) => {
                    error!("调用gRPC失败:{}",e);
                    
                    let text = format!("*Hacker News Top推送*: \n Comment Site:{}\n\n {}\n\n[{}]({})\n",
                                        tools::escape_markdown_v2(&url),
                                        "AI总结: 获取失败",
                                        "源内容网页: ",
                                        tools::escape_markdown_v2(&origin_news_url));
                    // 推送
                    tg_client.send_message(&text).await?;
                },
            }

            // 过滤完成，推送保存
            pushed_urls.write().await.insert(id, current_date.clone());
        }
        Ok(())
    }
    
}



#[cfg(test)]
mod tests{
    use std::error::Error;

    use super::*;
    use crate::config::Config;
    use crate::models::*;
    use crate::tgclient::*;
    use crate::tools;
    use crate::v2ex_client::*;
    use crate::llm::AIClient;
    use tokio;
    use reqwest::{Client,Proxy};
    use tokio::time::Duration;
    use teloxide::prelude::*;
    use chrono::prelude::*;




    #[tokio::test]
    async fn test_all() -> Result<(), Box<dyn Error>>{
        let config = Config::from_file("myconfig.toml");
        let base_date = Utc::now().format("%Y%m%d").to_string();
        let bot = Bot::new(&config.telegram.api_token);
        let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));
        let tg_client=TgClient::new(bot, chat_id);
        // let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
        let http_proxy = Proxy::http("http://127.0.0.1:5353")?;
        // 创建一个 HTTPS 代理
        let https_proxy = Proxy::https("http://127.0.0.1:5353")?;
        let http_client=Client::builder().proxy(http_proxy).proxy(https_proxy).build()?;

        // 新建gRPC客户端
        let channel = Channel::from_static("http://[::1]:50051")
        .connect_timeout(Duration::from_secs(5))  // 设置连接超时时间
        .timeout(Duration::from_secs(10))         // 设置调用超时时间
        .connect()
        .await?;
        let mut rpc_client = MyServiceClient::new(channel);

        // 新建ai客户端
        let ai_client=AIClient::new(&config.deepseek.api_token);

        let mut hacker_news=HackerNews::new(http_client,base_date.clone(),1,0);
        let mut shared_item=SharedItem::new();
        hacker_news.process_top_news(&mut shared_item,&mut rpc_client,&ai_client,&tg_client).await?;

        Ok(())
    }
}

