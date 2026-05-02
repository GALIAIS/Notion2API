use std::ffi::{c_char, CStr, CString};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::ptr;
use std::time::Duration;

use once_cell::sync::OnceCell;
use serde::{Deserialize, Serialize};
use tokio::runtime::Runtime;


static RUNTIME: OnceCell<Runtime> = OnceCell::new();

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
    body_b64: Option<String>,
    #[serde(default)]
    timeout_secs: Option<u64>,
}

#[derive(Serialize)]
struct ResponseEnvelope {
    ok: bool,
    status: u16,
    headers: Vec<(String, String)>,
    body_b64: String,
    final_url: String,
}

#[derive(Serialize)]
struct ErrorEnvelope<'a> {
    ok: bool,
    error: &'a str,
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
pub unsafe extern "C" fn wreq_request(
    client: *mut WreqClient,
    request_json: *const c_char,
) -> *mut c_char {
    catch_unwind(AssertUnwindSafe(|| {
        if client.is_null() || request_json.is_null() {
            return error_response("nil client or request");
        }
        let client = &*client;
        let raw = match CStr::from_ptr(request_json).to_str() {
            Ok(s) => s,
            Err(_) => return error_response("request_json: invalid utf-8"),
        };
        let spec: RequestSpec = match serde_json::from_str(raw) {
            Ok(s) => s,
            Err(e) => return error_response(&format!("request_json: {e}")),
        };
        runtime().block_on(do_request(&client.inner, spec))
    }))
    .unwrap_or_else(|_| error_response("rust panic in wreq_request"))
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


async fn do_request(client: &wreq::Client, spec: RequestSpec) -> *mut c_char {
    let method = match spec.method.parse::<wreq::Method>() {
        Ok(m) => m,
        Err(e) => return error_response(&format!("bad method: {e}")),
    };
    let mut req = client.request(method, &spec.url);
    for (k, v) in &spec.headers {
        req = req.header(k.as_str(), v.as_str());
    }
    if let Some(secs) = spec.timeout_secs {
        req = req.timeout(Duration::from_secs(secs));
    }
    if let Some(b64) = spec.body_b64.as_deref() {
        if !b64.is_empty() {
            match base64_decode(b64) {
                Ok(bytes) => req = req.body(bytes),
                Err(e) => return error_response(&format!("body_b64: {e}")),
            }
        }
    }

    let resp = match req.send().await {
        Ok(r) => r,
        Err(e) => return error_response(&format!("send: {e}")),
    };
    let status = resp.status().as_u16();
    let final_url = resp.url().to_string();
    let mut headers: Vec<(String, String)> = Vec::with_capacity(resp.headers().len());
    for (k, v) in resp.headers().iter() {
        headers.push((
            k.as_str().to_string(),
            v.to_str().unwrap_or("").to_string(),
        ));
    }
    let bytes = match resp.bytes().await {
        Ok(b) => b,
        Err(e) => return error_response(&format!("read body: {e}")),
    };
    let env = ResponseEnvelope {
        ok: true,
        status,
        headers,
        body_b64: base64_encode(&bytes),
        final_url,
    };
    json_to_c_string(&env)
}

fn error_response(msg: &str) -> *mut c_char {
    let env = ErrorEnvelope { ok: false, error: msg };
    json_to_c_string(&env)
}

fn json_to_c_string<T: Serialize>(value: &T) -> *mut c_char {
    let s = match serde_json::to_string(value) {
        Ok(s) => s,
        Err(_) => String::from("{\"ok\":false,\"error\":\"json serialize failed\"}"),
    };
    match CString::new(s) {
        Ok(c) => c.into_raw(),
        Err(_) => ptr::null_mut(),
    }
}


const B64_ALPHABET: &[u8; 64] =
    b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

fn base64_encode(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    let mut i = 0;
    while i + 3 <= bytes.len() {
        let n = ((bytes[i] as u32) << 16)
            | ((bytes[i + 1] as u32) << 8)
            | (bytes[i + 2] as u32);
        out.push(B64_ALPHABET[((n >> 18) & 0x3F) as usize] as char);
        out.push(B64_ALPHABET[((n >> 12) & 0x3F) as usize] as char);
        out.push(B64_ALPHABET[((n >> 6) & 0x3F) as usize] as char);
        out.push(B64_ALPHABET[(n & 0x3F) as usize] as char);
        i += 3;
    }
    let rem = bytes.len() - i;
    if rem == 1 {
        let n = (bytes[i] as u32) << 16;
        out.push(B64_ALPHABET[((n >> 18) & 0x3F) as usize] as char);
        out.push(B64_ALPHABET[((n >> 12) & 0x3F) as usize] as char);
        out.push('=');
        out.push('=');
    } else if rem == 2 {
        let n = ((bytes[i] as u32) << 16) | ((bytes[i + 1] as u32) << 8);
        out.push(B64_ALPHABET[((n >> 18) & 0x3F) as usize] as char);
        out.push(B64_ALPHABET[((n >> 12) & 0x3F) as usize] as char);
        out.push(B64_ALPHABET[((n >> 6) & 0x3F) as usize] as char);
        out.push('=');
    }
    out
}

fn base64_decode(input: &str) -> Result<Vec<u8>, &'static str> {
    let mut buf = Vec::with_capacity(input.len() * 3 / 4);
    let mut bits: u32 = 0;
    let mut nbits: u32 = 0;
    for c in input.bytes() {
        let v: u32 = match c {
            b'A'..=b'Z' => (c - b'A') as u32,
            b'a'..=b'z' => (c - b'a') as u32 + 26,
            b'0'..=b'9' => (c - b'0') as u32 + 52,
            b'+' | b'-' => 62,
            b'/' | b'_' => 63,
            b'=' | b'\n' | b'\r' | b' ' | b'\t' => continue,
            _ => return Err("invalid base64 char"),
        };
        bits = (bits << 6) | v;
        nbits += 6;
        if nbits >= 8 {
            nbits -= 8;
            buf.push(((bits >> nbits) & 0xFF) as u8);
        }
    }
    Ok(buf)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn b64_roundtrip() {
        for case in [&b""[..], b"f", b"fo", b"foo", b"foob", b"fooba", b"foobar"] {
            let enc = base64_encode(case);
            let dec = base64_decode(&enc).unwrap();
            assert_eq!(dec, case);
        }
    }
}
