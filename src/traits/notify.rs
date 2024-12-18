use std::error::Error;
use async_trait::async_trait;


// 定义通知接口
#[async_trait]
pub trait Notify {
    // 关联类型，用于指定 notify 返回的成功结果类型
    type Output;

    // 关联类型，用于指定方法可能返回的错误类型
    type NotifyError: Error;

    async fn notify(&self, content: String) -> Result<Self::Output, Self::NotifyError>;
}
