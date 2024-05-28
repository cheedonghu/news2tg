mod models;
mod v2ex_client;
mod config;
mod tgclient;
mod tools;
mod hackernews;
mod llm;

use config::*;
use hackernews::HackerNews;
use llm::AIClient;
use models::SharedItem;
use models::Topic;
use tgclient::TgClient;
use tokio;
use reqwest::Client;
use tokio::time::Duration;
use teloxide::prelude::*;
use chrono::prelude::*;
use chrono::Duration as ChronoDuration;
use std::error::Error;
use clap::Parser;
use v2ex_client::*;


/// 清理旧的url腾出内存空间， v2ex的清理五天前的， hacker news的清理15天前的
async fn clean_old_urls(shared_item: &mut SharedItem, current_date: &str){
    let v2ex_cutoff_date = DateTime::parse_from_str(current_date, "%Y%m%d")
        .unwrap()
        .checked_sub_signed(ChronoDuration::days(5))
        .unwrap();

    let hn_cutoff_date = DateTime::parse_from_str(current_date, "%Y%m%d")
        .unwrap()
        .checked_sub_signed(ChronoDuration::days(15))
        .unwrap();

    shared_item.v2ex_pushed_urls.write().await.retain(|_, date_str| {
        let date = DateTime::parse_from_str(date_str, "%Y%m%d").unwrap();
        date >= v2ex_cutoff_date
    });

    shared_item.hackernews_pushed_urls.write().await.retain(|_, date_str| {
        let date = DateTime::parse_from_str(date_str, "%Y%m%d").unwrap();
        date >= hn_cutoff_date
    });
}


#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    // 解析命令行参数
    let cli = Cli::parse();
    let config = Config::from_file(&cli.config);

    // 间隔时间
    let mut interval = tokio::time::interval(Duration::from_secs(90));
    // let client = Client::new();
    let bot = Bot::new(&config.telegram.api_token);
    let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));
    

    // 基准时间
    let mut base_date = Utc::now().format("%Y%m%d").to_string();
    let tg_client=TgClient::new(bot, chat_id);
    let mut v2ex_client=V2exClient::new(Client::new(), base_date.clone());
    let mut hackernews_client=HackerNews::new(Client::new(), base_date.clone());
    let ai_client=AIClient::new(&config.deepseek.api_token);
    let mut shared_item=SharedItem::new();

    
    loop {
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {
                println!("Received Ctrl+C, terminating...");
                break;
            }
            _ = interval.tick() => {
                let current_date = Utc::now().format("%Y%m%d").to_string();
                if current_date != base_date {
                    clean_old_urls(&mut shared_item, &current_date).await;
                    base_date = current_date.clone();
                    v2ex_client.update_current_date(&current_date);
                    hackernews_client.update_current_date(&current_date);
                }

                if config.features.v2ex_fetch_latest{
                    if let Err(e) = v2ex_client::fetch_latest_and_notify(&mut v2ex_client,&tg_client,&mut shared_item).await {
                        eprintln!("Error during fetch_latest_and_notify: {:?}", e);
                    }
                }

                if config.features.v2ex_fetch_hot{
                    if let Err(e) = v2ex_client::fetch_hotest_and_notify(&mut v2ex_client,&tg_client,&mut shared_item).await {
                        eprintln!("Error during fetch_hotest_and_notify: {:?}", e);
                    }
                }

                
                if config.features.hn_fetch_top{
                    if let Err(e) = hackernews::fetch_top_then_push(&mut hackernews_client,&tg_client,&ai_client,&mut shared_item).await {
                        eprintln!("Error during fetch_hotest_and_notify: {:?}", e);
                    }
                }
            }
        }
    }

    Ok(())
}