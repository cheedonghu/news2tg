use serde::Deserialize;
use std::fs;

#[derive(Deserialize)]
pub struct Features {
    pub fetch_latest: bool,
    pub fetch_hot: bool,
}
#[derive(Deserialize)]
pub struct TelegramConfig {
    pub api_token: String,
    pub chat_id: String,
}

#[derive(Deserialize)]
pub struct Config {
    pub features: Features,
    pub telegram: TelegramConfig,
}

impl Config {
    pub fn from_file(path: &str) -> Self {
        let config_str = fs::read_to_string(path).expect("Failed to read config file");
        toml::from_str(&config_str).expect("Failed to parse config file")
    }
}
