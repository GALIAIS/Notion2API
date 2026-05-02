
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
    println!("cargo:rerun-if-changed=build.rs");

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

    // Keep repository-local compatibility for cgo compile checks in environments
    // where include/wreq_ffi.h is ignored by git and cargo build is not runnable.
    if let Ok(workspace_root) = crate_dir.parent().map(std::path::Path::to_path_buf).ok_or(()) {
        let compat_header = workspace_root.join("internal").join("wreq").join("wreq_ffi_compat.h");
        let compat_body = r#"#ifndef WREQ_FFI_COMPAT_H
#define WREQ_FFI_COMPAT_H

#include <stdint.h>
#include <stddef.h>

typedef struct WreqClient WreqClient;
typedef struct WreqResponseHandle WreqResponseHandle;

int32_t wreq_request_begin(struct WreqClient *client,
                           const uint8_t *spec_json,
                           size_t spec_len,
                           const uint8_t *body_ptr,
                           size_t body_len,
                           struct WreqResponseHandle **out_handle,
                           uint16_t *out_status,
                           char **out_headers_json,
                           char **out_final_url,
                           char **out_error);
intptr_t wreq_response_read(struct WreqResponseHandle *handle,
                            uint8_t *buf,
                            size_t cap,
                            uint32_t timeout_ms);
void wreq_response_close(struct WreqResponseHandle *handle);

#endif /* WREQ_FFI_COMPAT_H */
"#;
        if let Some(parent) = compat_header.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let _ = std::fs::write(compat_header, compat_body);
    }
}
