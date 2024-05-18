use reqwest::{Client,Error};
use crate::models::Topic;
use crate::tgclient::{self, TgClient};
use std::collections::HashMap;


pub struct V2exClient{
    client: Client,
    tg_client: TgClient,
    pushed_urls: HashMap<String, String>,
    current_date: String,
    is_first: bool,
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
    is_first: bool
);


impl V2exClient {
    
    pub fn new(client: Client,tg_client: TgClient,pushed_urls: HashMap<String, String>,current_date: String) -> Self{
        V2exClient{client, tg_client, pushed_urls, current_date,is_first:true}
    }

    pub fn update_to_second(&mut self){
        self.is_first=false;
    }

    pub fn mutable_pushed_urls(&mut self)->&mut HashMap<String, String>{
        &mut self.pushed_urls
    }

}




pub async fn fetch_latest_topics(client: &Client) -> Result<Vec<Topic>, Error> {
    let url = "https://www.v2ex.com/api/topics/latest.json";
    let response = client.get(url).send().await?.json::<Vec<Topic>>().await?;
    // todo 不ok会怎样？
    Ok(response)
}

pub async fn fetch_hot_topics(client: &Client) -> Result<Vec<Topic>, Error> {
    let url = "https://www.v2ex.com/api/topics/hot.json";
    let response = client.get(url).send().await?.json::<Vec<Topic>>().await?;
    Ok(response)
}
