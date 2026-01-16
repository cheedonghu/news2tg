use chrono::prelude::*;
use chrono::Duration as ChronoDuration;
use clap::Parser;
use reqwest::Client;
use tokio;
use tokio::time::Duration;

use implementations::*;
use traits::news2tg::News2tg;
use crate::common::tools::escape_markdown_v2;
use crate::traits::notify::Notify;

use crate::common::config::Cli;
use crate::common::config::Config;

mod common;
mod implementations;
mod traits;

#[tokio::main]
async fn main() {
    // 解析命令行参数
    let cli = Cli::parse();
    let config = Config::from_file(&cli.config);

    // 初始化各大客户端
    let tg_client = NotifyTelegram::new(
        config.telegram.api_token.to_string(),
        config
            .telegram
            .chat_id
            .parse::<i64>()
            .expect("Invalid Tg chat id"),
    );
    let startup_text = format!("News2tg启动完成，监控任务开始投递内容。\n启动时间：[{}]\n项目地址：https://github.com/cheedonghu/news2tg", Local::now().format("%Y-%m-%d %H:%M").to_string());
    println!("{}", startup_text);
    if let Err(err) = tg_client.notify(& escape_markdown_v2(&startup_text)).await {
        eprintln!("Failed to send startup notification: {:?}", err);
    }
    // Startup fixed message
    let ai_client = AIHelperDeepSeek::new(config.deepseek.api_token.to_string());
    let http_client = Client::builder()
        .timeout(Duration::from_secs(5 * 60))
        .connect_timeout(Duration::from_secs(120))
        .build()
        .unwrap();
    let mut monitor_hn = MonitorHackerNews::new(http_client.clone(), tg_client, ai_client);

    let tg_client = NotifyTelegram::new(
        config.telegram.api_token.to_string(),
        config
            .telegram
            .chat_id
            .parse::<i64>()
            .expect("Invalid Tg chat id"),
    );
    // let http_client=Client::new();
    let mut monitor_v2ex = MonitorV2EX::new(http_client.clone(), tg_client);

    let config = Config::from_file(&cli.config);
    let hn_task_handle = tokio::spawn({
        let config = config.clone();
        async move { monitor_hn.run(&config).await }
    });
    let v2ex_task_handle = tokio::spawn({
        let config = config.clone();
        async move { monitor_v2ex.run(&config.clone()).await }
    });
    let ctrlc_task_handle = tokio::spawn(wait_for_ctrl_c());

    // 3. 等待这两个任务中的任何一个先结束
    tokio::select! {
        _ = hn_task_handle => {
            // 一般来说定时任务不会自己退出，如果它退出了说明发生了某种错误或 panic
            println!("HackerNews Interval task ended unexpectedly, shutting down...");
        },
        _ = v2ex_task_handle => {
            // 一般来说定时任务不会自己退出，如果它退出了说明发生了某种错误或 panic
            println!("V2EX Interval task ended unexpectedly, shutting down...");
        },
        _ = ctrlc_task_handle => {
            println!("Ctrl+C received, shutting down...");
        },
    }
}


pub async fn wait_for_ctrl_c() {
    tokio::signal::ctrl_c()
        .await
        .expect("Failed to install Ctrl+C handler");
    println!("Received Ctrl+C, terminating...");
}
