use std::fs;

use clap::Parser;
use serde::Deserialize;

#[derive(Parser, Debug)]
#[command(about = "Reads configuration from a file", long_about = None)]
pub struct Cli {
    /// Path to the configuration file
    #[arg(short, long, default_value="config.toml")]
    pub config: String,
    /// Path to output log file
    #[arg(short, long, default_value = "output.log")]
    pub output: String,
}


#[derive(Deserialize, Debug, Clone)]
pub struct Features {
    /// 新帖推送开关
    pub v2ex_fetch_latest: bool,
    /// 新帖推送关键字过滤
    pub v2ex_fetch_latest_keyword: Vec<String>,
    /// 热帖推送开关
    pub v2ex_fetch_hot: bool,
    /// hacker news top贴推送开关
    pub hn_fetch_top: bool,
    /// hacker news new贴推送开关
    pub hn_fetch_latest: bool,
    /// 每次拉取会有很多帖子，因此仅解析前n个的hacker news top帖子
    pub hn_fetch_num: usize,
    /// hacker news目标帖子距今时间（hacker news的top算法可能导致大量新帖冒出）
    pub hn_fetch_time_gap: usize,
}


#[derive(Deserialize, Debug, Clone)]
pub struct TelegramConfig {
    pub api_token: String,
    pub chat_id: String,
}

#[derive(Deserialize, Debug, Clone)]
pub struct DeepSeek {
    pub api_token: String
}

#[derive(Deserialize, Debug, Clone)]
pub struct Config {
    pub features: Features,
    pub telegram: TelegramConfig,
    pub deepseek: DeepSeek
}

impl Config {
    pub fn from_file(path: &str) -> Self {
        let config_str = fs::read_to_string(path).expect("Failed to read config file");
        toml::from_str(&config_str).expect("Failed to parse config file")
    }
}
