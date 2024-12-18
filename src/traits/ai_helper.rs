use async_trait::async_trait;
use crate::common::models::News2tgError;

// 定义 Monitor trait
#[async_trait]
pub trait AIHelper {
    // 关联类型，用于指定 fetch_hot 和 fetch_new 返回的成功结果类型
    type Output;

    // 利用大模型总结输入的内容
    async fn summarize(&self, content: String) -> Result<Self::Output, News2tgError>;

    async fn translate(&self) -> Result<Self::Output, News2tgError>;
}
