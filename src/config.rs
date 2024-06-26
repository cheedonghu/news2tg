use serde::Deserialize;
use std::fs;
use clap::Parser;

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


#[derive(Deserialize, Debug)]
pub struct Features {
    pub v2ex_fetch_latest: bool,
    pub v2ex_fetch_hot: bool,
    pub hn_fetch_top: bool,
    pub hn_fetch_num: usize,
}


#[derive(Deserialize, Debug)]
pub struct TelegramConfig {
    pub api_token: String,
    pub chat_id: String,
}

#[derive(Deserialize, Debug)]
pub struct DeepSeek {
    pub api_token: String
}

#[derive(Deserialize, Debug)]
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
