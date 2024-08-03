use log::{info, warn,error};
use news2tg::myservice::my_service_client::MyServiceClient;
use news2tg::myservice::ServiceRequest;
use tonic::transport::Channel;
use std::time::Duration;
use flexi_logger::{LogSpecification,LevelFilter, Duplicate, FileSpec, Logger, WriteMode, Criterion, Naming, Cleanup, detailed_format};
use flexi_logger::LoggerHandle;
use std::{collections::HashMap, env};


#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {

    // 配置 flexi_logger
    // Logger::try_with_str("debug")
    // .unwrap()
    // .log_to_file(FileSpec::default().directory("logs").basename("app")) // 将日志输出到文件
    // .write_mode(WriteMode::BufferAndFlush) // 设置日志写入模式
    // .duplicate_to_stdout(Duplicate::All)   // 同时输出到控制台
    // .rotate(
    //     Criterion::Size(10_000_000), // 设置日志文件大小限制为 10 MB
    //     Naming::Numbers,             // 使用数字序号进行文件命名
    //     Cleanup::KeepLogFiles(3),)
    // .format_for_files(detailed_format)     // 使用详细格式，包含时间戳
    // .start()
    // .unwrap();

    let channel = Channel::from_static("http://[::1]:50051")
        .connect_timeout(Duration::from_secs(5))  // 设置连接超时时间
        .timeout(Duration::from_secs(10))         // 设置调用超时时间
        .connect()
        .await?;
    
    let mut client = MyServiceClient::new(channel);

    // 建立与服务器的连接
    // let mut client: MyServiceClient<tonic::transport::Channel> = MyServiceClient::connect("http://[::1]:50051").await?;

    // 创建请求
    let request = tonic::Request::new(ServiceRequest {
        input: "Hello".into(),
    });

    // 调用远程函数
    let response = client.remote_function(request).await?;

    // 打印服务器响应
    println!("RESPONSE=\"{:?}\"", response.into_inner().output);

    Ok(())
}
