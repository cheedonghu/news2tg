use async_trait::async_trait;
use crate::common::config::Config;
use crate::common::models::News2tgError;
use crate::common::models::News2tgNotifyBase;

use super::ai_helper;
use super::ai_helper::AIHelper;
use super::monitor::Monitor;
use super::notify::Notify;


// 组装基类，当前整个工程的抽象逻辑为：获取待通知内容->ai处理（可选）->通知
#[async_trait]
pub trait News2tg {
    // 参数
    type Param;

    // 关联类型，
    type Output;

    // 一个辅助方法，用于获取基础结构体的引用
    // fn get_base(&mut self) -> &mut News2tgNotifyBase;

    // 定义抽象方法fetch/notify，这里只是方法签名
    async fn fetch(&mut self, config: &Config) -> Result<Self::Output, News2tgError>;

    // 这里要推送的数据采用传地址来处理还是采用成员变量处理？
    async fn ai_transfer(&mut self, param: Self::Output) -> Result<Self::Output, News2tgError>;

    async fn notify(&mut self, param:Self::Output) -> Result<bool, News2tgError>;

    // 核心组装方法，需要实现
    async fn run(&mut self, config: &Config) -> Result<(), News2tgError>;
}