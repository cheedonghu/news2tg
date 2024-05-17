mod models;
mod v2ex_client;
mod config;
mod telegram_client;

use config::Config;
use tokio;
use reqwest::Client;
use std::fs::File;
use std::io::{self, Write};
use tokio::time::Duration;

use v2ex_client::{fetch_latest_topics, fetch_hot_topics};
use telegram_client::send_telegram_message;

async fn run(client: &Client, config: &Config) -> io::Result<()> {
    let mut file = File::create("output.txt")?;

    if config.features.fetch_latest {
        match fetch_latest_topics(client).await {
            Ok(latest_topics) => {
                writeln!(file, "Latest topics:")?;
                write_topics(&mut file, latest_topics)?;
                send_telegram_message(bot.clone(), chat_id, String("tg push")).await.expect("Failed to send Telegram message");
            }
            Err(err) => {
                writeln!(file, "Error fetching latest topics: {:?}", err)?;
            }
        }
    } else {
        writeln!(file, "Fetching latest topics is disabled in the config.")?;
    }

    if config.features.fetch_hot {
        match fetch_hot_topics(client).await {
            Ok(hot_topics) => {
                writeln!(file, "Hot topics:")?;
                write_topics(&mut file, hot_topics)?;
                send_telegram_message(bot.clone(), chat_id, String("tg push")).await.expect("Failed to send Telegram message");
            }
            Err(err) => {
                writeln!(file, "Error fetching hot topics: {:?}", err)?;
            }
        }
    } else {
        writeln!(file, "Fetching hot topics is disabled in the config.")?;
    }

    Ok(())
}


fn write_topics(file: &mut File, topics: Vec<models::Topic>) -> io::Result<()> {
    for topic in topics {
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
    Ok(())
}



#[tokio::main]
async fn main() -> io::Result<()> {
    let config = Config::from_file("config.toml");
    let client = Client::new();
    let mut interval = tokio::time::interval(Duration::from_secs(60));
    let bot = Bot::new(&config.telegram.api_token);
    let chat_id = ChatId(config.telegram.chat_id.parse::<i64>().expect("Invalid chat ID"));


    loop {
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {
                println!("Received Ctrl+C, terminating...");
                break;
            }
            _ = interval.tick() => {
                if let Err(e) = run(&client, &config).await {
                    eprintln!("Error during run: {:?}", e);
                }
            }
        }
    }

    Ok(())
}