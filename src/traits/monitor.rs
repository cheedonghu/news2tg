use std::error::Error;
use reqwest::{Client};
use async_trait::async_trait;


use crate::models::Topic;

// 定义 Monitor trait
#[async_trait]
pub trait Monitor {
    // 关联类型，用于指定 fetch_hot 和 fetch_new 返回的成功结果类型
    type Output;

    // 关联类型，用于指定方法可能返回的错误类型
    type MonitorError: Error;

    // 定义 fetch_hot 方法
    async fn fetch_hot(&self) -> Result<Self::Output, Self::MonitorError>;

    // 定义 fetch_new 方法
    async fn fetch_new(&self) -> Result<Self::Output, Self::MonitorError>;
}
