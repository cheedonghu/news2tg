use news2tg::myservice::my_service_client::MyServiceClient;
use news2tg::myservice::ServiceRequest;


#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // 建立与服务器的连接
    let mut client = MyServiceClient::connect("http://[::1]:50051").await?;

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
