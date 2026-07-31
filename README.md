# Notion2API

一个基于 Go 的 Notion AI OpenAI 兼容桥接服务，提供标准 API、WebUI 管理面、多账号池和本地 SQLite 持久化，方便本地部署、调试和统一接入。

## 功能概览

- OpenAI 兼容接口：`/v1/models`、`/v1/chat/completions`、`/v1/responses`
- 支持流式响应
- 支持工具调用（`tools` / `tool_choice` / 旧版 `functions`），返回标准 `tool_calls`
- 可选内置 MCP 宿主：网关自己执行 MCP 工具，普通客户端无需感知
- 支持多账号池、批量导入、三种轮询策略、冷却退避与登录态刷新
- 支持图片、PDF、CSV 等附件请求
- 自带 WebUI 管理面：`/admin`，含测试区域（对话 / 工具调用 / MCP / 账号池自检）
- 使用 SQLite 持久化账号、会话和运行状态

## 快速开始

### 本地运行

```bash
go run ./cmd/notion2api --config ./config.example.json
```

### 本地构建

```bash
go build ./cmd/notion2api
```

## Docker 部署

先按实际环境修改 `config.docker.json`，再启动：

```bash
docker compose up -d --build
```

如果使用偏生产配置：

```bash
docker compose -f docker-compose.prod.yml up -d --build
```

本地从源码开发需 Go `1.25.0+`（`go.mod` 已声明）。

## 默认入口

- API：`http://127.0.0.1:8787/v1/*`
- Health：`http://127.0.0.1:8787/healthz`
- WebUI：`http://127.0.0.1:8787/admin`

## 代理与 Resin 粘性代理

### 代理模式

`proxy_mode` 支持：

- `off`：关闭代理
- `env`：从环境变量读取（优先 `N2A_*`）
- `http`：固定 HTTP 代理
- `https`：按协议拆分 HTTP/HTTPS 代理
- `socks5`：SOCKS5/SOCKS5H 代理
- `resin_forward`：Resin 粘性代理转发

### 环境变量优先级（`proxy_mode=env`）

HTTPS 请求优先顺序：

1. `N2A_PROXY_HTTPS_URL`
2. `N2A_UPSTREAM_PROXY_HTTPS_URL`
3. `N2A_PROXY_URL`
4. `N2A_UPSTREAM_PROXY_URL`
5. `HTTPS_PROXY` / `https_proxy`
6. `ALL_PROXY` / `all_proxy`

HTTP 请求优先顺序：

1. `N2A_PROXY_HTTP_URL`
2. `N2A_UPSTREAM_PROXY_HTTP_URL`
3. `N2A_PROXY_URL`
4. `N2A_UPSTREAM_PROXY_URL`
5. `HTTP_PROXY` / `http_proxy`
6. `ALL_PROXY` / `all_proxy`

也可以直接用环境变量覆盖配置文件中的代理字段：

- `N2A_PROXY_MODE`
- `N2A_PROXY_URL`
- `N2A_PROXY_HTTP_URL`
- `N2A_PROXY_HTTPS_URL`
- `N2A_RESIN_ENABLED`
- `N2A_RESIN_URL`
- `N2A_RESIN_PLATFORM`
- `N2A_RESIN_MODE`

### Resin 粘性代理（按账号隔离）

每个账号都可以独立设置粘性身份：

- `accounts[].sticky_proxy_account`：显式设置粘性账号名（推荐）
- 未设置时会回退到邮箱派生值

当启用 `resin_forward` 时：

- 代理认证用户名格式：`<resin_platform>.<sticky_proxy_account>`
- 密码使用 `resin_url` 中 token
- 请求会附带 `X-Resin-Account` 头

## 工具调用与 MCP 宿主

### 工具调用（`tools`）

网关兼容 OpenAI 的 `tools` / `tool_choice`，也兼容旧版 `functions` / `function_call`，并按标准形态返回 `tool_calls`。

`tools` 配置项：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `enabled` | `true` | 关闭后忽略请求里的 `tools` / `tool_choice` / `functions`，只走普通聊天 |
| `planning_mode` | `router` | `router`：单独一轮 JSON 决策，更稳；`native`：把工具契约注入提示词并从回答中解析，省一次上游请求 |
| `max_calls_per_turn` | `1` | 单轮最多产出的工具调用数，范围 1-16 |
| `max_rounds` | `16` | MCP 工具结果自动回灌的最大轮数，范围 1-128 |
| `result_char_limit` | `4000` | 单个工具结果注入提示词前的截断长度，最小 200 |
| `parallel_readonly` | `true` | 同一轮内的只读工具并发执行 |

执行归属的区分：

- 客户端在请求里声明的工具，网关只负责决策，结果以 `tool_calls` 返回给客户端执行
- MCP 工具由网关自己执行，结果回灌进下一轮，直到产出文本回答；普通客户端无需感知

### MCP 宿主（`mcp_servers`）

`mcp_servers` 是数组，每一项通过 stdio 启动一个 MCP 子进程，工具名以 `服务器名.工具名` 形式并入统一目录：

```json
"mcp_servers": [
  {
    "name": "fs",
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem", "./data"],
    "env": {},
    "enabled": true,
    "timeout_sec": 30,
    "auto_start": true
  }
]
```

> **安全边界**：启用一个 MCP 服务器，等于允许本网关在宿主机上执行对应命令，其权限与运行网关的用户一致。`mcp_servers` 默认为空数组，且每一项必须显式写 `enabled: true` 才会被启动。请只填写你自己审阅过的命令，不要把管理面暴露到不可信网络。

管理面 `/admin` 的「API Tester」页可以查看 MCP 服务器状态、工具目录，并手动触发单个工具调用与重载。

## 多账号调度

`dispatch.strategy` 支持三种轮询策略：

- `active_first`（默认）：优先当前激活账号，受限时才轮换
- `round_robin`：在可用账号之间顺序轮询
- `least_used`：优先挑选历史调用量最少的账号

以下账号会被自动跳过：已禁用、状态为 `pending_code`、处于冷却退避中、小时额度已耗尽、并发槽位已满。失败会按指数退避写入冷却时间。

批量导入通过 `POST /admin/accounts/batch`（单次最多 50 条），或直接在管理面「账号」页的「批量导入账号」卡片操作，支持三种输入混用：

- `emails`：邮箱列表（换行 / 逗号 / 分号分隔），逐个发起验证码登录，之后在「验证码登录管线」里补验证码
- `text`：批量文本，段与段之间用只含 `---` 或 `===` 的行分隔
- `items`：完整导入请求对象数组

`text` 里的单行 JSON 会按 JSONL 逐行处理。每段会自动识别类型：含 `cookie_header` / `probe_json_text` 的 JSON 视为完整导入请求；含 `email` / `user_id` / `space_id` / `client_version` / `cookies` 的 JSON 视为 Probe JSON；纯文本含 `token_v2` 等 cookie 名则视为 cookie header。可选的顶层 `active` 在全部条目处理完后把指定邮箱切为当前账号。

返回 `{success, processed, succeeded, failed, items:[{email, ok, action, detail}], accounts}`，`action` 取值为 `login_started` / `imported` / `activated`。整批共享一次配置落盘。

## 配置说明

建议优先检查这些字段：

- `api_key`：OpenAI 兼容接口密钥
- `admin.password`：WebUI 登录密码
- `upstream_base_url` / `upstream_origin`
- `proxy_mode` / `proxy_url` / `proxy_http_url` / `proxy_https_url`
- `resin_enabled` / `resin_url` / `resin_platform` / `resin_mode`
- `accounts[*].sticky_proxy_account`
- `accounts` / `active_account`
- `dispatch.strategy`
- `tools.enabled` / `tools.planning_mode` / `tools.max_rounds`
- `mcp_servers`（默认为空；启用即授予本机命令执行权限）
- `storage.sqlite_path`

可直接参考：

- `config.example.json`
- `config.docker.json`

## 使用建议

- 首次启动后先访问 `/admin`，确认账号、配置和连通性是否正常
- 管理面「API Tester」页自带可用性自检（账号池 / MCP / 对话 / 工具调用），排障时先跑一遍
- 修改管理台前端后需执行 `npm --prefix ./frontend run build:static`
- 若构建卡在字体下载（`socket hang up` 后反复重试），用 `NODE_OPTIONS=--dns-result-order=ipv4first` 再跑一次
- 调整会话延续与存储时，建议同步检查 `internal/app/sqlite_store.go` 的 schema 与迁移兼容性

## 开源协议

MIT License

## 致谢

本项目已在 [LINUX DO 社区](https://linux.do) 发布，感谢社区的支持与反馈。
