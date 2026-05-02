use std::ffi::{c_char, CStr, CString};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr;
use std::slice;
use std::sync::{Mutex, OnceLock};
use std::time::Duration;

use serde::Deserialize;
use tokio::runtime::Runtime;

const WREQ_OK: i32 = 0;
const WREQ_ERR_NIL_ARG: i32 = -1;
const WREQ_ERR_BAD_UTF8: i32 = -2;
const WREQ_ERR_BAD_JSON: i32 = -3;
const WREQ_ERR_BAD_METHOD: i32 = -4;
const WREQ_ERR_SEND: i32 = -5;
const WREQ_ERR_HEADERS_JSON: i32 = -6;
const WREQ_ERR_BODY_READ: i32 = -7;
const WREQ_ERR_CLOSED: i32 = -8;
const WREQ_ERR_TIMEOUT: i32 = -9;
const WREQ_ERR_PANIC: i32 = -100;

static RUNTIME: OnceLock<Runtime> = OnceLock::new();

fn runtime() -> &'static Runtime {
    RUNTIME.get_or_init(|| {
        tokio::runtime::Builder::new_multi_thread()
            .enable_all()
            .thread_name("wreq-ffi")
            .build()
            .expect("wreq-ffi: failed to create tokio runtime")
    })
}

pub struct WreqClient {
    inner: wreq::Client,
}

struct WreqResponseState {
    response: Option<wreq::Response>,
    pending: Vec<u8>,
}

pub struct WreqResponseHandle {
    state: Mutex<WreqResponseState>,
}

#[derive(Default, Deserialize)]
struct ClientConfig {
    #[serde(default)]
    emulation: Option<String>,
    #[serde(default)]
    timeout_secs: Option<u64>,
    #[serde(default)]
    proxy_url: Option<String>,
}

#[derive(Deserialize)]
struct RequestSpec {
    method: String,
    url: String,
    #[serde(default)]
    headers: Vec<(String, String)>,
    #[serde(default)]
    timeout_secs: Option<u64>,
}

#[inline]
fn c_string_from_message(message: impl AsRef<str>) -> *mut c_char {
    match CString::new(message.as_ref()) {
        Ok(text) => text.into_raw(),
        Err(_) => ptr::null_mut(),
    }
}

#[inline]
unsafe fn clear_out_error(out_error: *mut *mut c_char) {
    if !out_error.is_null() {
        *out_error = ptr::null_mut();
    }
}

#[inline]
unsafe fn set_out_error(out_error: *mut *mut c_char, message: impl AsRef<str>) {
    if out_error.is_null() {
        return;
    }
    *out_error = c_string_from_message(message);
}

#[inline]
unsafe fn clear_begin_outputs(
    out_handle: *mut *mut WreqResponseHandle,
    out_status: *mut u16,
    out_headers_json: *mut *mut c_char,
    out_final_url: *mut *mut c_char,
) {
    if !out_handle.is_null() {
        *out_handle = ptr::null_mut();
    }
    if !out_status.is_null() {
        *out_status = 0;
    }
    if !out_headers_json.is_null() {
        *out_headers_json = ptr::null_mut();
    }
    if !out_final_url.is_null() {
        *out_final_url = ptr::null_mut();
    }
}

#[no_mangle]
pub unsafe extern "C" fn wreq_client_new(profile_json: *const c_char) -> *mut WreqClient {
    catch_unwind(AssertUnwindSafe(|| {
        let cfg: ClientConfig = if profile_json.is_null() {
            ClientConfig::default()
        } else {
            let s = match CStr::from_ptr(profile_json).to_str() {
                Ok(s) => s,
                Err(_) => return ptr::null_mut::<WreqClient>(),
            };
            match serde_json::from_str(s) {
                Ok(c) => c,
                Err(_) => return ptr::null_mut(),
            }
        };

        let mut builder = wreq::Client::builder();
        if let Some(secs) = cfg.timeout_secs {
            builder = builder.timeout(Duration::from_secs(secs));
        }
        if let Some(p) = cfg.proxy_url.as_deref() {
            if let Ok(proxy) = wreq::Proxy::all(p) {
                builder = builder.proxy(proxy);
            }
        }
        let _ = cfg.emulation;

        match builder.build() {
            Ok(client) => Box::into_raw(Box::new(WreqClient { inner: client })),
            Err(_) => ptr::null_mut(),
        }
    }))
    .unwrap_or(ptr::null_mut())
}

#[no_mangle]
pub unsafe extern "C" fn wreq_client_free(client: *mut WreqClient) {
    if !client.is_null() {
        drop(Box::from_raw(client));
    }
}

#[no_mangle]
pub unsafe extern "C" fn wreq_request_begin(
    client: *mut WreqClient,
    spec_json: *const u8,
    spec_len: usize,
    body_ptr: *const u8,
    body_len: usize,
    out_handle: *mut *mut WreqResponseHandle,
    out_status: *mut u16,
    out_headers_json: *mut *mut c_char,
    out_final_url: *mut *mut c_char,
    out_error: *mut *mut c_char,
) -> i32 {
    clear_out_error(out_error);
    clear_begin_outputs(out_handle, out_status, out_headers_json, out_final_url);

    let result = catch_unwind(AssertUnwindSafe(|| {
        if client.is_null()
            || out_handle.is_null()
            || out_status.is_null()
            || out_headers_json.is_null()
            || out_final_url.is_null()
        {
            set_out_error(out_error, "nil client or output pointer");
            return WREQ_ERR_NIL_ARG;
        }
        if spec_json.is_null() && spec_len > 0 {
            set_out_error(out_error, "spec_json is null but spec_len > 0");
            return WREQ_ERR_NIL_ARG;
        }
        if body_ptr.is_null() && body_len > 0 {
            set_out_error(out_error, "body_ptr is null but body_len > 0");
            return WREQ_ERR_NIL_ARG;
        }

        let spec_bytes = slice::from_raw_parts(spec_json, spec_len);
        let spec: RequestSpec = match serde_json::from_slice(spec_bytes) {
            Ok(value) => value,
            Err(err) => {
                set_out_error(out_error, format!("request_json: {err}"));
                return WREQ_ERR_BAD_JSON;
            }
        };

        let method = match spec.method.parse::<wreq::Method>() {
            Ok(method) => method,
            Err(err) => {
                set_out_error(out_error, format!("bad method: {err}"));
                return WREQ_ERR_BAD_METHOD;
            }
        };

        let client = &*client;
        let mut req = client.inner.request(method, &spec.url);
        for (key, value) in &spec.headers {
            req = req.header(key.as_str(), value.as_str());
        }
        if let Some(secs) = spec.timeout_secs {
            req = req.timeout(Duration::from_secs(secs));
        }
        if body_len > 0 {
            let body = slice::from_raw_parts(body_ptr, body_len);
            req = req.body(body.to_vec());
        }

        let resp = match runtime().block_on(req.send()) {
            Ok(response) => response,
            Err(err) => {
                set_out_error(out_error, format!("send: {err}"));
                return WREQ_ERR_SEND;
            }
        };

        let status = resp.status().as_u16();
        let final_url = resp.url().to_string();
        let mut headers: Vec<(String, String)> = Vec::with_capacity(resp.headers().len());
        for (key, value) in resp.headers().iter() {
            headers.push((
                key.as_str().to_string(),
                value.to_str().unwrap_or("").to_string(),
            ));
        }

        let headers_json = match serde_json::to_string(&headers) {
            Ok(raw) => raw,
            Err(err) => {
                set_out_error(out_error, format!("headers json encode failed: {err}"));
                return WREQ_ERR_HEADERS_JSON;
            }
        };
        let headers_c = match CString::new(headers_json) {
            Ok(value) => value,
            Err(_) => {
                set_out_error(out_error, "headers json contains interior NUL");
                return WREQ_ERR_HEADERS_JSON;
            }
        };
        let final_url_c = match CString::new(final_url) {
            Ok(value) => value,
            Err(_) => {
                set_out_error(out_error, "final_url contains interior NUL");
                return WREQ_ERR_BAD_UTF8;
            }
        };

        let handle = Box::new(WreqResponseHandle {
            state: Mutex::new(WreqResponseState {
                response: Some(resp),
                pending: Vec::new(),
            }),
        });

        *out_handle = Box::into_raw(handle);
        *out_status = status;
        *out_headers_json = headers_c.into_raw();
        *out_final_url = final_url_c.into_raw();
        WREQ_OK
    }));

    match result {
        Ok(code) => code,
        Err(_) => {
            set_out_error(out_error, "rust panic in wreq_request_begin");
            WREQ_ERR_PANIC
        }
    }
}

#[no_mangle]
pub unsafe extern "C" fn wreq_response_read(
    handle: *mut WreqResponseHandle,
    buf: *mut u8,
    cap: usize,
    timeout_ms: u32,
) -> isize {
    let result = catch_unwind(AssertUnwindSafe(|| {
        if handle.is_null() {
            return WREQ_ERR_NIL_ARG as isize;
        }
        if cap == 0 {
            return 0;
        }
        if buf.is_null() {
            return WREQ_ERR_NIL_ARG as isize;
        }

        let handle = &*handle;
        let mut state = match handle.state.lock() {
            Ok(guard) => guard,
            Err(poisoned) => poisoned.into_inner(),
        };

        if !state.pending.is_empty() {
            let write_len = state.pending.len().min(cap);
            ptr::copy_nonoverlapping(state.pending.as_ptr(), buf, write_len);
            state.pending.drain(..write_len);
            return write_len as isize;
        }

        let response = match state.response.as_mut() {
            Some(response) => response,
            None => return WREQ_ERR_CLOSED as isize,
        };

        let chunk = if timeout_ms == 0 {
            runtime().block_on(response.chunk())
        } else {
            match runtime().block_on(tokio::time::timeout(
                Duration::from_millis(timeout_ms as u64),
                response.chunk(),
            )) {
                Ok(res) => res,
                Err(_) => return WREQ_ERR_TIMEOUT as isize,
            }
        };

        match chunk {
            Ok(Some(bytes)) => {
                let raw = bytes.as_ref();
                let write_len = raw.len().min(cap);
                ptr::copy_nonoverlapping(raw.as_ptr(), buf, write_len);
                if write_len < raw.len() {
                    state.pending.extend_from_slice(&raw[write_len..]);
                }
                write_len as isize
            }
            Ok(None) => 0,
            Err(_) => WREQ_ERR_BODY_READ as isize,
        }
    }));

    match result {
        Ok(n) => n,
        Err(_) => WREQ_ERR_PANIC as isize,
    }
}

#[no_mangle]
pub unsafe extern "C" fn wreq_response_close(handle: *mut WreqResponseHandle) {
    if handle.is_null() {
        return;
    }
    let mut boxed = Box::from_raw(handle);
    if let Ok(state) = boxed.state.get_mut() {
        state.pending.clear();
        state.response = None;
    }
}

#[no_mangle]
pub unsafe extern "C" fn wreq_string_free(ptr: *mut c_char) {
    if !ptr.is_null() {
        drop(CString::from_raw(ptr));
    }
}

#[no_mangle]
pub extern "C" fn wreq_ffi_version() -> *const c_char {
    static VERSION: &[u8] = concat!(env!("CARGO_PKG_VERSION"), "\0").as_bytes();
    VERSION.as_ptr() as *const c_char
}
