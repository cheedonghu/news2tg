use reqwest::{Client,Error};
use crate::models::{SharedItem, Topic};
use crate::tgclient:: TgClient;
use std::io::{self};


use crate::tools::*;

pub struct V2exClient{
    client: Client,
    // pushed_urls: HashMap<String, String>,
    current_date: String,
    is_latest_first: bool,
    is_hotest_first: bool
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

// 使用宏为 V2exClient 结构体生成 getter 函数
create_getters!(
    V2exClient,
    client: Client,
    // pushed_urls: HashMap<String, String>,
    current_date: String,
    is_latest_first: bool,
    is_hotest_first: bool
);


impl V2exClient {
    
    pub fn new(client: Client,current_date: String) -> Self{
        V2exClient{client, current_date,is_latest_first:true,is_hotest_first:true}
    }

    pub fn update_latest_to_second(&mut self){
        self.is_latest_first=false;
    }

    pub fn update_hotest_to_second(&mut self){
        self.is_hotest_first=false;
    }

    // pub fn mutable_pushed_urls(&mut self)->&mut HashMap<String, String>{
    //     &mut self.pushed_urls
    // }

    pub fn update_current_date(&mut self, new_date: &str){
        self.current_date=String::from(new_date);
    }

    pub async fn fetch_latest_topics(&self) -> Result<Vec<Topic>, Error> {
        let url = "https://www.v2ex.com/api/topics/latest.json";
        let response = self.client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await?.json::<Vec<Topic>>().await?;
        Ok(response)
    }
    
    pub async fn fetch_hot_topics(&self) -> Result<Vec<Topic>, Error> {
        let url = "https://www.v2ex.com/api/topics/hot.json";
        let response = self.client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await?.json::<Vec<Topic>>().await?;
        Ok(response)
    }


}


/// 清洗获取的帖子内容，保留最新的部分
pub async fn format_topics_message(
    shared_item :&mut SharedItem,
    section_title: &str,
    current_date: &str,
    topics: &[Topic],
) -> Vec<String> {
    // let mut message: String = format!("{}:\n", section_title);
    // let mut message: String=String::new();
    let mut new_topic_message=Vec::new();
    for topic in topics {
        if !shared_item.v2ex_pushed_urls.read().await.contains_key(&topic.url) {
            let title=truncate_utf8(&topic.title, 4000);
            let title=escape_markdown_v2(&title);
            // message.push_str(&format!("*{}*: [{}]({})\n",section_title, topic.title, topic.url));
            new_topic_message.push(format!("*{}*: [{}]({})\n",section_title, title, topic.url));
            shared_item.v2ex_pushed_urls.write().await.insert(topic.url.clone(), current_date.to_string());
        }
    }
    new_topic_message
}

/// 拉取最新贴并推送
pub async fn fetch_latest_and_notify(
    v2ex_client: &mut V2exClient, 
    tg_client:&TgClient, 
    shared_item:&mut SharedItem) -> io::Result<()> {
    let topics=v2ex_client.fetch_latest_topics().await.unwrap();
    let section_title="新贴推送";

    let mut new_message_array=Vec::new();

    if !topics.is_empty() {
        new_message_array=format_topics_message(shared_item, section_title, v2ex_client.current_date(),&topics).await;
        if new_message_array.is_empty(){
            println!("暂无新贴")
        }
    }

    if !new_message_array.is_empty()&&!*v2ex_client.is_latest_first() {
        if let Err(err) = tg_client.send_batch_message(&new_message_array).await {
            eprintln!("Failed to send Telegram message: {:?}", err);
        }
    }

    // 开启推送
    v2ex_client.update_latest_to_second();

    Ok(())
}


/// 拉取最热贴并推送
pub async fn fetch_hotest_and_notify(    
    v2ex_client: &mut V2exClient, 
    tg_client:&TgClient, 
    shared_item:&mut SharedItem) -> io::Result<()> {
    let topics=v2ex_client.fetch_hot_topics().await.unwrap();
    let section_title="热帖推送";

    let mut new_message_array=Vec::new();

    if !topics.is_empty() {
        new_message_array=format_topics_message(shared_item, section_title, v2ex_client.current_date(),&topics).await;
        if new_message_array.is_empty(){
            println!("[v2ex] 暂无新贴")
        }
    }

    if !new_message_array.is_empty()&&!*v2ex_client.is_hotest_first() {
        if let Err(err) = tg_client.send_batch_message(&new_message_array).await {
            eprintln!("Failed to send Telegram message: {:?}", err);
        }
    }

    v2ex_client.update_hotest_to_second();

    Ok(())
}


#[cfg(test)]
mod tests{
    use std::error::Error;

    use super::*;
    use chrono::Utc;
    use crate::config::Config;

   
    #[tokio::test]
    async fn test_url(){
        // let mut base_date = Utc::now().format("%Y%m%d").to_string();
        // let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
        let config = Config::from_file("myconfig.toml");
        let client=Client::new();
        
        let url = "http://www.v2ex.com/api/topics/hot.json";
        // let origin_response=client
        // .get(url)
        // .header("User-Agent", "PostmanRuntime/7.37.3")
        // .send().await.expect("Failed to send request");
        // let text = origin_response.text().await.expect("Failed to read response text");
        // println!("Raw response: {}", text);
        
        // let url = "https://www.v2ex.com/api/v2/nodes/hot";
        // let origin_response=client
        // .get(url)
        // .header("Authorization", &config.v2ex.token)
        // .send().await.expect("Failed to send request");
        // let text = origin_response.text().await.expect("Failed to read response text");
        // println!("Raw response: {}", text);


        let response = match client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await {
            Ok(resp) => match resp.json::<Vec<Topic>>().await {
                Ok(json) => {
                    println!("get json successfully, the first is {:?}", json.get(0).unwrap());
                    json
                },
                Err(err) => {
                    eprintln!("Error parsing JSON: {:?}", err);
                    return;
                }
            },
            Err(err) => {
                eprintln!("Error sending request: {:?}", err);
                return;
            }
        };

    }
                       // 返回内容
}