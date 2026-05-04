# T-4-2 HTTP Client / Transport Audit

Date: 2026-05-02

## Scope checked

- `internal/app/notion_client.go`
- `internal/app/login_helper.go`
- `internal/app/notion_client_login_transport.go`
- `internal/app/account_discovery.go`
- `internal/app/session_refresh.go`

## Findings

### 1) NotionAI request path creates new `http.Client`/`Transport` per `NotionAIClient`

- Location: `internal/app/notion_client.go:newNotionAIClientWithMode`
- Behavior:
  - Builds a fresh `http.Transport` and `http.Client` every time a `NotionAIClient` is created.
  - In dispatch paths (`runPromptWithSession*`) this can happen frequently, so connection pools are not reused across those client instances.
- Impact:
  - Potentially higher connect/TLS handshake overhead under sustained traffic.
  - Extra pressure on upstream and local sockets due to fragmented pools.

### 2) Login helper path also creates fresh `http.Client`

- Location: `internal/app/login_helper.go:newNotionLoginSession`
- Behavior:
  - Creates a new cookie jar and `http.Client` per login session call.
- Notes:
  - This path is less hot than inference path, but still relevant for repeated refresh/login workflows.

### 3) Proxy/header behavior correctness constraints

- Proxy resolution and resin headers are request/account dependent:
  - `ProxyResolver.ResolveProxyForRequest(accountEmail, targetURL)` can vary by account/policy.
  - `postJSONResponse` overlays per-request proxy headers (e.g. resin account header).
- Any reuse strategy must preserve:
  - account-aware proxy resolution
  - per-request header injection behavior
  - stream vs non-stream timeout difference

## Recommendation

Introduce a transport cache in `internal/app/notion_client.go`:

- Cache key dimensions:
  - normalized upstream base/origin/host/tls server name
  - proxy mode + proxy urls + resin settings
  - account email key (for account-specific proxy routing)
  - streaming flag is **not** required in transport key (timeout is on `http.Client`, not transport)
- Cache value:
  - reusable `*http.Transport`
- Then construct short-lived `http.Client` wrappers over cached transport:
  - standard client timeout = request timeout
  - streaming client timeout = 0
- Add evidence:
  - metric for transport/client creation count
  - benchmark around repeated client creation path if needed

## Current status

- Updated 2026-05-02 follow-up:
  - Implemented transport cache in `newNotionAIClientWithMode` via keyed map + RWMutex.
  - Added runtime visibility metric: `notion2api_http_transport_cache_total` (`hit_rlock`, `hit_lock`, `miss_new`) exposed through `/debug/vars`.
  - Added tests validating:
    - same account/config => transport reuse
    - different account proxy policy => transport separation
  - Added benchmark `BenchmarkNewNotionAIClientWithModeTransportCache` showing warm-cache path lower alloc/op and ns/op than forced cold-cache path.
