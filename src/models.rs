use serde::Deserialize;
use tokio::sync::RwLock;
use std::collections::HashMap;
use std::error::Error;
use std::fmt::{Display,Formatter,Result};

/// 自定义错误
#[derive(Debug)]
pub struct MyError {
    pub message: String,
}

impl Display for MyError {
    fn fmt(&self, f: &mut Formatter) -> Result {
        write!(f, "Custom Error: {}", self.message)
    }
}

impl Error for MyError {}


/// v2ex响应结构
#[derive(Deserialize, Debug)]
pub struct Node {
    pub avatar_large: Option<String>,
    pub name: String,
    pub avatar_normal: Option<String>,
    pub title: String,
    pub url: String,
    pub topics: u64,
    pub footer: Option<String>,
    pub header: Option<String>,
    pub title_alternative: Option<String>,
    pub avatar_mini: Option<String>,
    pub stars: u64,
    pub aliases: Vec<String>,
    pub root: bool,
    pub id: u64,
    pub parent_node_name: Option<String>,
}

/// v2ex响应结构
#[derive(Deserialize, Debug)]
pub struct Member {
    pub id: u64,
    pub username: String,
    pub url: String,
    pub website: Option<String>,
    pub twitter: Option<String>,
    pub psn: Option<String>,
    pub github: Option<String>,
    pub btc: Option<String>,
    pub location: Option<String>,
    pub tagline: Option<String>,
    pub bio: Option<String>,
    pub avatar_mini: Option<String>,
    pub avatar_normal: Option<String>,
    pub avatar_large: Option<String>,
    pub created: u64,
    pub last_modified: u64,
}

/// v2ex响应结构
#[derive(Deserialize, Debug)]
pub struct Topic {
    pub node: Node,
    pub member: Member,
    pub last_reply_by: Option<String>,
    pub last_touched: u64,
    pub title: String,
    pub url: String,
    pub created: u64,
    pub deleted: u64,
    pub content: Option<String>,
    pub content_rendered: Option<String>,
    pub last_modified: u64,
    pub replies: u64,
    pub id: u64,
}


/// 程序共享变量
pub struct SharedItem{
    pub v2ex_pushed_urls: RwLock<HashMap<String,String>>,
    pub hackernews_pushed_urls: RwLock<HashMap<String,String>>
}

impl SharedItem {
    pub fn new() -> SharedItem{
        SharedItem{
            v2ex_pushed_urls: RwLock::new(HashMap::new()),
            hackernews_pushed_urls: RwLock::new(HashMap::new())
        }
    }
}

