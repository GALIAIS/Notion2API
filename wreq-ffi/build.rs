
use std::env;
use std::path::PathBuf;

fn main() {
    let crate_dir = env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR set by cargo");
    let crate_dir = PathBuf::from(crate_dir);
    let lib_rs = crate_dir.join("src").join("lib.rs");
    let header_path = crate_dir.join("include").join("wreq_ffi.h");

    if let Some(parent) = header_path.parent() {
        std::fs::create_dir_all(parent).expect("create include dir");
    }

    println!("cargo:rerun-if-changed=src/lib.rs");
    println!("cargo:rerun-if-changed=cbindgen.toml");

    let config = cbindgen::Config::from_file(crate_dir.join("cbindgen.toml"))
        .unwrap_or_else(|_| cbindgen::Config::default());

    let bindings = cbindgen::Builder::new()
        .with_src(&lib_rs)
        .with_config(config)
        .generate()
        .unwrap_or_else(|e| {
            panic!(
                "wreq-ffi: cbindgen failed to generate header from {}: {e}",
                lib_rs.display()
            )
        });

    bindings.write_to_file(&header_path);
}
