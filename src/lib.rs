pub mod grpc;


pub mod myservice {
    include!("./proto-gen/myservice.rs"); // The string specified here must match the proto package name
}