// use tonic::{transport::Server, Request, Response, Status};
// use myservice::my_service_server::{MyService, MyServiceServer};
// use myservice::{HelloRequest, HelloResponse};

// struct MockMyService;

// #[tonic::async_trait]
// impl MyService for MockMyService {
//     async fn remote_function(
//         &self,
//         request: Request<ServiceRequest>,
//     ) -> Result<Response<ServiceResponse>, Status> {
//         let input = request.into_inner().input;
//         let output = format!("Processed: {}", input);
//         Ok(Response::new(ServiceResponse { output }))
//     }
// }

// #[tokio::test]
// async fn test_process_request() {
//     // Start the mock server
//     let addr = "[::1]:50051".parse().unwrap();
//     let service = MyServiceServer::new(MockMyService);

//     tokio::spawn(async move {
//         Server::builder()
//             .add_service(service)
//             .serve(addr)
//             .await
//             .unwrap();
//     });

//     // Give the server a moment to start
//     tokio::time::sleep(tokio::time::Duration::from_millis(100)).await;

//     // Test the client
//     let result = process_request("Test input".to_string()).await.unwrap();
//     assert_eq!(result, "Processed: Test input");
// }
