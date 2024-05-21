use reqwest::{Client,Error};
use crate::models::Topic;
use crate::tgclient::{self, TgClient};
use std::collections::HashMap;
use std::io::{self, Write};


use crate::tools::*;

pub struct V2exClient{
    client: Client,
    tg_client: TgClient,
    pushed_urls: HashMap<String, String>,
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
    tg_client: TgClient,
    pushed_urls: HashMap<String, String>,
    current_date: String,
    is_latest_first: bool,
    is_hotest_first: bool
);


impl V2exClient {
    
    pub fn new(client: Client,tg_client: TgClient,pushed_urls: HashMap<String, String>,current_date: String) -> Self{
        V2exClient{client, tg_client, pushed_urls, current_date,is_latest_first:true,is_hotest_first:true}
    }

    pub fn update_latest_to_second(&mut self){
        self.is_latest_first=false;
    }

    pub fn update_hotest_to_second(&mut self){
        self.is_hotest_first=false;
    }

    pub fn mutable_pushed_urls(&mut self)->&mut HashMap<String, String>{
        &mut self.pushed_urls
    }

    pub fn update_current_date(&mut self, new_date: &str){
        self.current_date=String::from(new_date);
    }

    pub async fn fetch_latest_topics(&self) -> Result<Vec<Topic>, Error> {
        let url = "https://www.v2ex.com/api/topics/latest.json";
        let response = self.client.get(url).send().await?.json::<Vec<Topic>>().await?;
        Ok(response)
    }
    
    pub async fn fetch_hot_topics(&self) -> Result<Vec<Topic>, Error> {
        let url = "https://www.v2ex.com/api/topics/hot.json";
        let response = self.client.get(url).send().await?.json::<Vec<Topic>>().await?;
        Ok(response)
    }

    
    /// 清洗获取的帖子内容，保留最新的部分
    pub fn format_topics_message(
        &mut self,
        section_title: &str,
        topics: &[Topic],
    ) -> Vec<String> {
        // let mut message: String = format!("{}:\n", section_title);
        // let mut message: String=String::new();
        let mut new_topic_message=Vec::new();
        for topic in topics {
            if !self.pushed_urls.contains_key(&topic.url) {
                let title=truncate_utf8(&topic.title, 4000);
                let title=escape_markdown_v2(&title);
                // message.push_str(&format!("*{}*: [{}]({})\n",section_title, topic.title, topic.url));
                new_topic_message.push(format!("*{}*: [{}]({})\n",section_title, title, topic.url));
                self.pushed_urls.insert(topic.url.clone(), self.current_date.to_string());
            }
        }
        new_topic_message
    }


    /// 拉取最新贴并推送
    pub async fn fetch_latest_and_notify(&mut self) -> io::Result<()> {
        let topics=self.fetch_latest_topics().await.unwrap();
        let section_title="Latest topics";

        let mut new_message_array=Vec::new();

        if !topics.is_empty() {
            write_topics_to_file(section_title, &topics,self.pushed_urls())?;
            new_message_array=self.format_topics_message(section_title,&topics);
            if new_message_array.is_empty(){
                println!("暂无新贴")
            }
        }

        if !new_message_array.is_empty()&&!*self.is_latest_first() {
            if let Err(err) = self.tg_client().send_batch_message(&new_message_array).await {
                eprintln!("Failed to send Telegram message: {:?}", err);
            }
        }

        // 开启推送
        self.update_latest_to_second();

        Ok(())
    }


    /// 拉取最热贴并推送
    pub async fn fetch_hotest_and_notify(&mut self) -> io::Result<()> {
        let topics=self.fetch_hot_topics().await.unwrap();
        let section_title="Hot topics";

        let mut new_message_array=Vec::new();

        if !topics.is_empty() {
            write_topics_to_file(section_title, &topics,self.pushed_urls())?;
            new_message_array=self.format_topics_message(section_title,&topics);
            if new_message_array.is_empty(){
                println!("暂无新的热贴")
            }
        }

        if !new_message_array.is_empty()&&!*self.is_hotest_first() {
            if let Err(err) = self.tg_client().send_batch_message(&new_message_array).await {
                eprintln!("Failed to send Telegram message: {:?}", err);
            }
        }

        self.update_hotest_to_second();

        Ok(())
    }


}

