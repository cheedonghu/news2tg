use std::error::Error;
use std::fs;

static OUT_DIR: &str = "src/grpc";

fn main() -> Result<(), Box<dyn Error>> {
    let protos = [
        "grpc_proto/python_digest.proto",
    ];

    fs::create_dir_all(OUT_DIR).unwrap();
    tonic_build::configure()
        .build_server(true)
        .out_dir(OUT_DIR)
        .compile(&protos, &["grpc/"])?;

    rerun(&protos);

    Ok(())
}

fn rerun(proto_files: &[&str]) {
    for proto_file in proto_files {
        println!("cargo:rerun-if-changed={}", proto_file);
    }
}