fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_build::configure()
        .build_server(false)
        .compile_protos(&["service.proto"], &["."])?;
    println!("cargo:rerun-if-changed=service.proto");
    Ok(())
}
