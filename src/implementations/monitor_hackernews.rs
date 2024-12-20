use std::error::Error;
use futures::TryFutureExt;
use reqwest::Client;
use async_trait::async_trait;
use crate::traits::monitor::Monitor;
use crate::tokio::sync::RwLock;
use std::collections::HashMap;
use crate::Local;

use crate::common::models::{News2tgError, Topic,News2tgNotifyBase};
use crate::traits::news2tg::News2tg;
use crate::common::config::Config;
use crate::common::tools;
use scraper::{Html,Selector};

use crate::traits::ai_helper::AIHelper;
use crate::traits::notify::Notify;
use crate::grpc::digest::{digest_client::DigestClient,digest_server::DigestServer};
use crate::grpc::digest::ServiceRequest;
use tonic::transport::Channel;
use tokio::time::Duration;

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
pub struct MonitorHackerNews<N: Notify, A: AIHelper> {
    http_client: Client,
    pushed_urls: RwLock<HashMap<String, String>>,
    notify_client: N,
    ai_client: A,
    digest_client: DigestClient<Channel>
}


impl<N: Notify, A: AIHelper> MonitorHackerNews<N,A> {
    pub fn new(http_client: Client, notify_client: N, ai_client: A, digest_client: DigestClient<Channel>) -> Self{
        MonitorHackerNews{
            http_client: http_client,
            pushed_urls: RwLock::new(HashMap::new()),
            notify_client,
            ai_client,
            digest_client,
        }

        
    }

    pub fn get_pushed_urls(&mut self) -> &mut RwLock<HashMap<String, String>>{
        &mut self.pushed_urls
    }

    /// 从网页里面判断发帖时间
    fn judge_news_date(&self, response:&str, time_gap:usize) -> bool{
        let now=Local::now();
        println!("{} 开始判断帖子日期是否在范围内", now.format("%Y年%m月%d日 %H:%M:%S"));
        // Parse the HTML
        let document = Html::parse_document(response);
        let selector = Selector::parse("tbody tr td.subtext span.age").unwrap();

        // document.select(&selector)
        let mut title_time:Option<String>=None;
        for element in document.select(&selector) {
            let text = element.text().collect::<Vec<_>>().join(" ");

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
    pub fn get_news_origin_url(&self, response:&str) -> Result<String,Box<dyn Error>>{
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

    /// 调用gRPC，从pyhton获取网页摘要
    async fn get_digest_from_python(&mut self, origin_news_url: &str)-> Result<String, Box<dyn Error>>{
        println!("{} 开始调用gRPC接口获取源网址摘要", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
        // 创建请求
        let request = tonic::Request::new( ServiceRequest{
            input: origin_news_url.to_string(),
        });

        // 调用远程函数
        match self.digest_client.remote_function(request).await {
            Ok(response) => {
                let digest=response.into_inner().output;
                // 打印服务器响应
                // info!("调用gRPC结果：\"{:?}\"", digest);
                println!("{} 调用python网页摘要接口结果：\"{:?}\"", Local::now().format("%Y年%m月%d日 %H:%M:%S"),digest);
                // 返回结果
                Ok(digest)
            },
            Err(e) => {
                // error!("调用gRPC失败: {:?}", e);
                println!("{} 调用python网页摘要接口失败", Local::now().format("%Y年%m月%d日 %H:%M:%S"));
                Err(Box::new(e))
            },
        }
    }

    /// 根据hacker news帖子id解析网页获取相关数据和网页摘要
    async fn process(&mut self, id: String, time_gape: usize) -> Option<News2tgNotifyBase>{
        if self.pushed_urls.read().await.contains_key(&id){
            // 已推送的不处理
            println!("当前id:{} 已推送", id);
            return Option::None;
        }
        let now=Local::now();
        println!("{} 开始解析id: {}", &now.format("%Y年%m月%d日 %H:%M:%S"), &id);
        // 新出现的
        let url=format!("https://news.ycombinator.com/item?id={}",&id);
        let response=self.http_client
        .get(url.clone()).send()
        .map_err(|err| News2tgError::RuntimeError("获取hackernews帖子内容请求发送失败".to_string()))
        .await.unwrap()
        .text().map_err(|err| News2tgError::RuntimeError("提取hackernews帖子文本失败".to_string())).await.unwrap();

        // 仅创建时间不算短的才继续解析推送否则推送频率太高
        if !self.judge_news_date(&response, time_gape) {
            // 不满足要求，当前url跳过
            return Option::None;
        }

        // ai总结：1. 获取源信息url 2.获取url链接内容 3.发送给大模型进行总结
        let origin_news_url=self.get_news_origin_url(&response).unwrap();

        // 从python那边获取网页摘要 todo 摘要后续可以改为接口
        let mut output=News2tgNotifyBase::default();
        match self.get_digest_from_python(&origin_news_url).await {
            Ok(digest) => {
                output.set_url(url);
                output.set_origin_url(origin_news_url);
                output.set_content(digest);
            },
            Err(_err) => {
                output.set_url(url);
                output.set_origin_url(origin_news_url);
                // 无需ai翻译
                output.set_content_transfered_by_ai_flag(false);
                output.set_content("网页摘要获取失败".to_string());
            },
        }

        // 过滤完成，推送保存
        self.pushed_urls.write().await.insert(id, (&now).format("%Y%m%d").to_string());
        Some(output)
    }
}

// 实现 Monitor trait for MonitorHackerNews
#[async_trait]
impl<N: Notify+ Send + Sync, A: AIHelper+Send+Sync> Monitor for MonitorHackerNews<N,A> {
    type Output = Vec<String>;
    type MonitorError = MonitorHackerNewsError;

    async fn fetch_hot(&self) -> Result<Self::Output, Self::MonitorError> {
        let url = "https://hacker-news.firebaseio.com/v0/topstories.json?print=pretty";

        let result = match self.http_client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
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

        let result = match self.http_client.get(url).header("User-Agent", "PostmanRuntime/7.37.3").send().await{
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

#[async_trait]
impl<N: Notify+ Send + Sync, A: AIHelper+Send+Sync> News2tg for MonitorHackerNews<N,A> 
where A: AIHelper<Output = String>
{
    type Param = ();
    type Output = Vec<News2tgNotifyBase>;

    /// 按配置文件中的规则调用monitor接口获取需要的内容
    async fn fetch(&mut self, config: &Config) -> Result<Self::Output, News2tgError>{
        let mut result:Vec<News2tgNotifyBase>=Vec::new();
        let mut hot_topics: Vec<String>=Vec::new();
        let mut new_topics: Vec<String>=Vec::new();
        if config.features.hn_fetch_top{
            hot_topics=self.fetch_hot().await.unwrap().into();
            // 根据配置处理前n个帖子
            hot_topics=hot_topics.iter().take(config.features.hn_fetch_num.clone()).cloned().collect();
        }
        if config.features.hn_fetch_latest{
            new_topics=self.fetch_new().await.unwrap().into();
            new_topics=new_topics.iter().take(config.features.hn_fetch_num.clone()).cloned().collect();
        }
        let hot_title="Hacker News 热帖推送";
        let new_title="Hacker News 新帖推送";

        // 处理热帖
        for id in hot_topics{
            if let Some(mut output) = self.process(id, config.features.hn_fetch_time_gap).await{
                output.set_title(hot_title.to_string());
                result.push(output);
            }
        }

        // 处理新帖
        for id in new_topics{
            if let Some(mut output) = self.process(id, config.features.hn_fetch_time_gap).await{
                output.set_title(new_title.to_string());
                result.push(output);
            }
        }

        Ok(result)
    }

    async fn ai_transfer(&mut self, output_list: Self::Output) -> Result<Self::Output, News2tgError>{
        // Implementation here
        let mut result:Vec<News2tgNotifyBase>= Vec::new();
        // 将需要ai翻译的拿出来
        for mut output in output_list{
            if !*output.content_transfered_by_ai_flag(){
                // 无需AI翻译，直接格式化
                let format = format!("*{}*: \n Comment Site:{}\n\n {}\n\n[{}]({})\n", 
                output.title(),
                tools::escape_markdown_v2(&output.url()), 
                format!("AI总结: {}", tools::escape_markdown_v2(&output.content())),
                "源内容网页: ", tools::escape_markdown_v2(&output.origin_url()));
                output.set_content(format);
            }else {
                // 需要AI总结
                let content_transfered_by_ai: String=self.ai_client
                .summarize(output.content().to_string()).await.unwrap();
                let format = format!("*{}*: \n Comment Site:{}\n\n {}\n\n[{}]({})\n", 
                output.title(),
                tools::escape_markdown_v2(&output.url()), 
                format!("AI总结: {}", tools::escape_markdown_v2(&content_transfered_by_ai)),
                "源内容网页: ", tools::escape_markdown_v2(&output.origin_url()));
                output.set_content(format);
            }
            result.push(output);
        }
 
        Ok(result)
    }

    async fn notify(&mut self, param: Self::Output) -> Result<bool, News2tgError>{
        // let content:&Vec<News2tgNotifyBase> = param;
        // Implementation here
        
        let contents:Vec<String>=param.iter().map(|item| item.content().clone()).collect();

        let _ = self.notify_client.notify_batch(&contents).await;

        Ok(true)
    }

    /// 这里决定该监控类用哪个ai和推送到哪
    async fn run(&mut self, config: &Config) -> Result<Self::Output, News2tgError> {
        let result: Vec<News2tgNotifyBase>=match self.fetch(config).await {
            Ok(output)=> output,
            Err(err)=> {
                eprintln!("获取hacker news信息失败");
                return Err(err);
            }
        };

        if result.capacity()>0{
            let result =self.ai_transfer(result).await.unwrap();
            let _ =self.notify(result).await;
        }
        
        Ok(Vec::new())
    }
}




#[cfg(test)]
mod tests{

    use super::*;
    use crate::common::config::Config;
    use crate::implementations::ai_helper_deepseek::AIHelperDeepSeek;
    use crate::implementations::notify_tg::NotifyTelegram;

   
    #[tokio::test]
    async fn test_url(){
        let config = Config::from_file("myconfig.toml");

            // 新建gRPC客户端
        let channel = Channel::from_static("http://[::1]:50051")
        .connect_timeout(Duration::from_secs(5))  // 设置连接超时时间
        .timeout(Duration::from_secs(10))         // 设置调用超时时间
        .connect()
        .await
        .map_err(|err| format!(
            "与python工程摘要接口建立连接失败: {:?}",err))
        .unwrap();
        let rpc_client = DigestClient::new(channel);

        let http_client=Client::new();
        let tg_client=NotifyTelegram::new(config.telegram.api_token.to_string(), config.telegram.chat_id.parse::<i64>().expect("Invalid Tg chat id"));
        let ai_client=AIHelperDeepSeek::new(config.deepseek.api_token.to_string());

        let mut monitor=MonitorHackerNews::new(http_client, tg_client, ai_client, rpc_client);
        
        monitor.run(&config).await;

        // println!("result is :{:?}", result.get(0))

    }
}





