//! Compiles `proto/metap_crud.proto` — using a vendored `protoc` binary + its bundled
//! well-known-type `.proto` includes (`protoc-bin-vendored`) rather than requiring either on
//! `PATH`, so building this crate doesn't depend on whatever protobuf tooling happens to be
//! installed on a given dev machine or CI runner (found live: this workspace's dev environment
//! has no system `protoc` at all).

fn main() {
    let protoc_path = protoc_bin_vendored::protoc_bin_path().expect("failed to locate vendored protoc binary");
    std::env::set_var("PROTOC", protoc_path);
    let include_path = protoc_bin_vendored::include_path().expect("failed to locate vendored protoc well-known types");
    std::env::set_var("PROTOC_INCLUDE", include_path);

    tonic_prost_build::configure()
        .compile_protos(&["proto/metap_crud.proto"], &["proto"])
        .expect("failed to compile proto/metap_crud.proto");
}
