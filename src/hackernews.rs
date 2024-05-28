use reqwest::{Client,Error};
use crate::llm::AIClient;
use crate::models::SharedItem;
use crate::tgclient::TgClient;
use std::collections::HashMap;
use scraper::{Html,Selector};
use tokio::sync::RwLock;
use crate::tools::truncate_utf8;

pub struct HackerNews{
    client: Client,
    current_date: String,
    push_enabled: bool
}

impl HackerNews{

    pub fn new(client: Client,current_date: String) -> Self{
        HackerNews{client, current_date,push_enabled:false}
    }

    // pub fn new(client: Client,tg_client: TgClient,pushed_urls: HashMap<String, String>,current_date: String) -> Self{
    //     HackerNews{client, tg_client, pushed_urls, current_date,push_enabled:false}
    // }

    /// 打开推送开关
    pub fn enable_push(&mut self){
        self.push_enabled=true;
    }

    // pub fn mutable_pushed_urls(&mut self)->&mut HashMap<String, String>{
    //     &mut self.pushed_urls
    // }

    // /// 获取已推送url
    // pub fn add_pushed_urls(&mut self, new_urls: HashMap<String,String>){
    //     for(key,value) in new_urls{
    //         self.pushed_urls.insert(key, value);
    //     }
    // }

    /// 更新当前时间
    pub fn update_current_date(&mut self, new_date: &str){
        self.current_date=String::from(new_date);
    }

    /// 从hacker news的comment页面中提取出源网址： 在titleline的href内
    pub async fn get_news_origin_url(&self, url:&str) -> Result<String,Error>{
        let mut news_url: String=String::new();

        let response=self.client.get(url).send().await?.text().await?;
        // Parse the HTML
        let document = Html::parse_document(&response);
        let selector = Selector::parse("span.titleline a").unwrap();

        // Extract the link
        if let Some(element) = document.select(&selector).next() {
            if let Some(href) = element.value().attr("href") {
                println!("Extracted URL: {}", href);
                news_url=String::from(href);
            } else {
                println!("No href attribute found");
            }
        } else {
            println!("No matching elements found");
        }
        
        Ok(news_url)
    }

    /// 调用hacker news的top分类接口
    async fn get_hacker_news_top_info(&self) -> Result<Vec<String>,Error>{
        let url = "https://hacker-news.firebaseio.com/v0/newstories.json?print=pretty";
        let response = self.client.get(url).send().await?.json::<Vec<String>>().await?;
        Ok(response)
    }

}

// 宏定义
macro_rules! create_getters {
    ($struct_name:ident, $($field_name:ident: $field_type:ty),*) => {
        impl $struct_name {
            $(
                pub fn $field_name(&self) -> &$field_type {
                    &self.$field_name
                }
            )*
        }
    };
}

// 使用宏为 HackerNews 结构体生成 getter 函数
create_getters!(
    HackerNews,
    client: Client,
    current_date: String,
    push_enabled: bool
);



/// 获取前10的id，如果有新上榜的就推送
pub async fn fetch_top_then_push(
    hacker_news: &mut HackerNews, 
    tg_client: &TgClient, 
    ai_client: &AIClient,
    shared_item: &mut SharedItem) -> Result<(),Error>{
    // 最新id数组
    let id_array=hacker_news.get_hacker_news_top_info().await.unwrap();

    // 拿出前10的id
    let truncated_id: Vec<String>=id_array.iter().take(10).cloned().collect();

    // 将新出现的保存并推送
    let current_date=hacker_news.current_date().to_string();
    let pushed_urls: &mut RwLock<HashMap<String,String>>=&mut shared_item.hackernews_pushed_urls;

    // 初次启动
    if !hacker_news.push_enabled(){
        let write_pushed_urls=&mut pushed_urls.write().await;
        for id in truncated_id{
            write_pushed_urls.insert(id, current_date.clone());
        }
        hacker_news.enable_push();
    }else{
        // 开始推送
        for id in truncated_id{
            // 新出现的
            if !pushed_urls.read().await.contains_key(&id){
                let url=format!("https://news.ycombinator.com/item?id={}",&id);
                // 保存
                pushed_urls.write().await.insert(id, current_date.clone());
    
                // ai总结：1. 获取源信息url 2.获取url链接内容 3.发送给大模型进行总结
                let origin_news_url=hacker_news.get_news_origin_url(&url).await.unwrap();
                let origin_news_content=hacker_news.client().get(origin_news_url).send().await?.text().await?;
                let summary=ai_client.summarize(&origin_news_content).await.unwrap();
                let summary2=truncate_utf8(&summary,2000);
    
                // 格式化消息
                let text=&format!("*{}*: [{}]({})\n","Hacker News推送", summary2, url);
                // 推送
                tg_client.send_message(text).await;
            }
        }
    }    
    Ok(())
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
        let bot = Bot::new(&config.telegram.api_token);
        let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));


        let tg_client=TgClient::new(bot, chat_id);
        // let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
        let hackernews_client=HackerNews::new(Client::new(), base_date.clone());
        let ai_client=AIClient::new(&config.deepseek.api_token);
        let mut shared_item=SharedItem::new();

        let new_runtime = Runtime::new()?;
        // 同步执行
        new_runtime.block_on(async {
            // fetch_top_then_push(&mut hackernews_client,&tg_client,&ai_client,&mut shared_item).await;

            let pushed_urls: &mut RwLock<HashMap<String,String>>=&mut shared_item.hackernews_pushed_urls;
            let url=format!("https://news.ycombinator.com/item?id={}",40484591);
            // 保存
            pushed_urls.write().await.insert(String::from("40484591"), base_date.clone());

            // ai总结：1. 获取源信息url 2.获取url链接内容 3.发送给大模型进行总结
            let origin_news_url=hackernews_client.get_news_origin_url(&url).await.unwrap();

            println!("{}", &origin_news_url);

            let origin_news_content=hackernews_client.client().get(origin_news_url).send().await.unwrap().text().await.unwrap();
            
            let mut file = File::create("output.txt").unwrap();
            writeln!(file, "{}", &origin_news_content).unwrap();

            // let summary=ai_client.summarize(&origin_news_content).await.unwrap();
            // let summary2=truncate_utf8(&summary,2000);

            // // 格式化消息
            // let text=&format!("*{}*: [{}]({})\n","Hacker News推送", summary2, url);
            // // 推送
            // tg_client.send_message(text).await;

        });
        // if let Err(err) = tgclient.send_telegram_message("test").await {
        //     eprintln!("Failed to send Telegram message: {:?}", err);
        // }
        Ok(())
    }
}

