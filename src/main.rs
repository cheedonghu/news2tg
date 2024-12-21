mod grpc;
mod traits;
mod implementations;
mod common;

use crate::common::config::Config;
use crate::common::config::Cli;
use clap::Parser;
use implementations::*;
use tokio;
use reqwest::Client;
use tokio::time::Duration;
use chrono::prelude::*;
use chrono::Duration as ChronoDuration;
use traits::news2tg::News2tg;
use tonic::transport::Channel;
use crate::grpc::digest::digest_client::DigestClient;

pub async fn wait_for_ctrl_c() {
    tokio::signal::ctrl_c()
        .await
        .expect("Failed to install Ctrl+C handler");
    println!("Received Ctrl+C, terminating...");
}


#[tokio::main]
async fn main(){
    // 解析命令行参数
    let cli = Cli::parse();
    let config = Config::from_file(&cli.config);

    // 初始化各大客户端
    let channel = Channel::from_static("http://[::1]:50051")
    .connect_timeout(Duration::from_secs(5))  // 设置连接超时时间
    .timeout(Duration::from_secs(10))         // 设置调用超时时间
    .connect()
    .await
    .map_err(|err| format!(
        "与python工程摘要接口建立连接失败: {:?}",err))
    .unwrap();
    let rpc_client = DigestClient::new(channel);
    let tg_client=NotifyTelegram::new(config.telegram.api_token.to_string(), config.telegram.chat_id.parse::<i64>().expect("Invalid Tg chat id"));
    let ai_client=AIHelperDeepSeek::new(config.deepseek.api_token.to_string());
    let http_client=Client::new();
    let mut monitor_hn=MonitorHackerNews::new(http_client, tg_client, ai_client, rpc_client);

    let tg_client=NotifyTelegram::new(config.telegram.api_token.to_string(), config.telegram.chat_id.parse::<i64>().expect("Invalid Tg chat id"));
    let http_client=Client::new();
    let mut monitor_v2ex=MonitorV2EX::new(http_client, tg_client );

    let config = Config::from_file(&cli.config);
    let hn_task_handle = tokio::spawn({
        let config=config.clone();
        async move {monitor_hn.run(&config).await}
    });
    let v2ex_task_handle = tokio::spawn({
        let config=config.clone();
        async move {monitor_v2ex.run(&config.clone()).await}
    });
    let ctrlc_task_handle = tokio::spawn(wait_for_ctrl_c());

    // 3. 等待这两个任务中的任何一个先结束
    tokio::select! {
        _ = hn_task_handle => {
            // 一般来说定时任务不会自己退出，如果它退出了说明发生了某种错误或 panic
            println!("Interval task ended unexpectedly, shutting down...");
        },
        _ = v2ex_task_handle => {
            // 一般来说定时任务不会自己退出，如果它退出了说明发生了某种错误或 panic
            println!("Interval task ended unexpectedly, shutting down...");
        },
        _ = ctrlc_task_handle => {
            println!("Ctrl+C received, shutting down...");
        },
    }

}