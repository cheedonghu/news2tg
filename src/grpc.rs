// use tonic::{transport::Channel, Request};
// use myservice::my_service_client::MyServiceClient;
// use myservice::ServiceRequest;

// pub mod myservice {
//     // tonic::include_proto!("myservice");
//     include!("./proto-gen/service.rs");
// }

// async fn call_remote_function(client: &mut MyServiceClient<Channel>, input: String) -> Result<String, Box<dyn std::error::Error>> {
//     let request = Request::new(ServiceRequest { input });
//     let response = client.remote_function(request).await?;
//     println!("RESPONSE={:?}", response);
//     Ok(response.into_inner().output)
// }

// pub async fn process_request(input: String) -> Result<String, Box<dyn std::error::Error>> {
//     let channel = Channel::from_static("http://[::1]:50051").connect().await?;
//     let mut client = MyServiceClient::new(channel);

//     call_remote_function(&mut client, input).await
// }