mod models;
mod v2ex_client;
mod config;
mod tgclient;
mod tools;

use config::Config;
use models::Topic;
use tgclient::TgClient;
use tokio;
use reqwest::Client;
use std::fs::File;
use std::io::{self, Write};
use tokio::time::Duration;
use teloxide::prelude::*;
use std::collections::HashMap;
use chrono::prelude::*;
use chrono::Duration as ChronoDuration;

use v2ex_client::{fetch_hot_topics, fetch_latest_topics, V2exClient};

async fn fetch_and_notify(
    current_date: &str,
    fetch_latest: bool,
    v2ex_client: &mut V2exClient
) -> io::Result<()> {
    let topics: Vec<Topic>;
    let section_title;
    if fetch_latest {
        topics=fetch_latest_topics(v2ex_client.client()).await.unwrap();
        section_title="Latest topics";
    }else{
        topics=fetch_hot_topics(v2ex_client.client()).await.unwrap();
        section_title="Hot topics";
    }

    let mut message = String::new();

    if !topics.is_empty() {
        write_topics_to_file(section_title, &topics,v2ex_client.pushed_urls())?;
        let section_message = format_topics_message(section_title, &topics, v2ex_client.mutable_pushed_urls(), current_date);
        if !section_message.is_empty() {
            message.push_str(&section_message);
        }else{
            println!("暂无新的帖子")
        }
    }

    if !message.is_empty()&&!*v2ex_client.is_first() {
        if let Err(err) = v2ex_client.tg_client().send_telegram_message( message).await {
            eprintln!("Failed to send Telegram message: {:?}", err);
        }
    }

    v2ex_client.update_to_second();

    Ok(())
}


fn write_topics_to_file(section_title: &str, topics: &[models::Topic], pushed_urls: & HashMap<String, String>) -> io::Result<()> {
    let mut file = File::create("output.txt")?;
    writeln!(file, "{}:", section_title)?;
    for topic in topics {
        if !pushed_urls.contains_key(&topic.url){
            writeln!(file, "ID: {}", topic.id)?;
            writeln!(file, "Title: {}", topic.title)?;
            writeln!(file, "URL: {}", topic.url)?;
            if let Some(content) = &topic.content {
                writeln!(file, "Content: {}", content)?;
            }
            writeln!(file, "Replies: {}", topic.replies)?;
            writeln!(file, "Member: {} (ID: {})", topic.member.username, topic.member.id)?;
            writeln!(file, "Node: {} (ID: {})", topic.node.title, topic.node.id)?;
            writeln!(file)?;
        }
    }
    Ok(())
}

fn format_topics_message(
    section_title: &str,
    topics: &[models::Topic],
    pushed_urls: &mut HashMap<String, String>,
    current_date: &str
) -> String {
    // let mut message: String = format!("{}:\n", section_title);
    let mut message: String=String::new();
    for topic in topics {
        if !pushed_urls.contains_key(&topic.url) {
            message.push_str(&format!("*Title*: [{}]({})\n", topic.title, topic.url));
            pushed_urls.insert(topic.url.clone(), current_date.to_string());
        }
    }
    message
}


fn clean_old_urls(pushed_urls: &mut HashMap<String, String>, current_date: &str) {
    let cutoff_date = DateTime::parse_from_str(current_date, "%Y%m%d")
        .unwrap()
        .checked_sub_signed(ChronoDuration::days(5))
        .unwrap();

    pushed_urls.retain(|_, date_str| {
        let date = DateTime::parse_from_str(date_str, "%Y%m%d").unwrap();
        date >= cutoff_date
    });
}


#[tokio::main]
async fn main() -> io::Result<()> {
    // 间隔时间
    let mut interval = tokio::time::interval(Duration::from_secs(60));
    let config = Config::from_file("config.toml");

    let client = Client::new();
    let bot = Bot::new(&config.telegram.api_token);
    let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));

    let pushed_urls: HashMap<String, String> = HashMap::new();
    // 五天前
    let mut last_date = Utc::now().format("%Y%m%d").to_string();


    let mut v2ex_client=V2exClient::new(client, TgClient::new(bot, chat_id), pushed_urls, last_date.clone());

    loop {
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {
                println!("Received Ctrl+C, terminating...");
                break;
            }
            _ = interval.tick() => {
                let current_date = Utc::now().format("%Y%m%d").to_string();
                if current_date != last_date {
                    clean_old_urls(v2ex_client.mutable_pushed_urls(), &current_date);
                    last_date = current_date.clone();
                }

                if config.features.fetch_latest{
                    if let Err(e) = fetch_and_notify( &current_date, true,&mut v2ex_client).await {
                        eprintln!("Error during fetch_and_notify: {:?}", e);
                    }
                }

                if config.features.fetch_hot{
                    if let Err(e) = fetch_and_notify( &current_date, false,&mut  v2ex_client).await {
                        eprintln!("Error during fetch_and_notify: {:?}", e);
                    }
                }
            }
        }
    }

    Ok(())
}