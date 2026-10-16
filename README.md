# AllProxyAPI

一个本地统一 LLM 反代网关，把三个 AI 编程客户端的私有后端逆向为标准的 **OpenAI** 与 **Anthropic** API 协议：

| 上游 | 后端 | 协议形态 | 模型族 |
|---|---|---|---|
| **Google Antigravity** | Cloud Code PA (`v1internal`) | Gemini + 外层 envelope | Gemini 2.5/3.x、Claude 4.x |
| **Tencent WorkBuddy** | CodeBuddy (`/v2/chat/completions`) | OpenAI 兼容 | DeepSeek、Kimi、GLM、Hunyuan |
| **Alibaba Qoder** | `api2-v2.qoder.sh` / `gateway.qoder.com.cn` | OpenAI 兼容 | Qwen、Kimi、GLM、DeepSeek、MiniMax |

Go 1.27 单二进制，内嵌 Web 控制面板，零外部运行时依赖，常驻内存约 20MB。

---

## 架构

设计上让 **router 保持无厂商分支**：每个上游是一个自包含的 adapter，新增 provider 只需加一个文件 + 一条注册。

```
client ──► ingress ──► router ──► Upstream adapter ──► 上游
                        │              │
                        │              └─ 认证头 / wire envelope / 模型命名 / 响应解包
                        └─ 选号、故障转移、协议转换（OpenAI ⇄ Anthropic ⇄ Gemini）
```

```
cmd/proxy/                  进程入口：装配 registry、刷新器、HTTP server
internal/
  config/                   配置 schema 与默认值
  pool/                     账号池：类型化账号、轮询选号、冷却、凭据扫描
  ingress/
    upstream.go             Upstream 接口 + Registry + 路由解析 + 响应转发
    adapters.go             三个 provider 的 adapter 实现
    router.go               HTTP 路由、鉴权、面板 API
    translator.go           OpenAI ⇄ Gemini ⇄ Anthropic 协议转换
    schema.go               Gemini 工具参数 schema 净化
    openai/, anthropic/     下游协议类型定义
  egress/
    antigravity/            Cloud Code PA 客户端 + envelope 解包
    workbuddy/              双 region 客户端 + 身份指纹头
    qoder/                  region 端点 + 凭据交换/刷新 + 模型别名
  web/                      内嵌控制面板
  webtools/                 可选的联网搜索注入
```

**核心抽象**是 `ingress.Upstream`：

```go
type Upstream interface {
    ID() pool.AccountType
    Label() string
    Format() ResponseFormat                                     // OpenAI | Gemini 帧格式
    DefaultModels() []string
    FetchModels(ctx, *pool.Account) ([]pool.ModelMetadata, error)
    Chat(ctx, *pool.Account, *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, error)
    Unwrap(io.Reader) (io.Reader, error)
}
```

路由器只调用这个接口，因此它不知道 Gemini envelope、`X-Machine-ID`、COSY 签名之类的存在。

---

## 快速开始

```bash
go build -o allproxyapi.exe ./cmd/proxy
cp data/config.example.yaml data/config.yaml   # 按需修改
./allproxyapi.exe --config data/config.yaml
```

默认监听 `http://127.0.0.1:8320`，控制面板在 `http://127.0.0.1:8320/`。

```
--config <path>   配置文件路径（默认 ./data/config.yaml）
--port <n>        覆盖监听端口
--host <addr>     覆盖监听地址
```

---

## 客户端接入

任选一种协议把客户端指向本网关即可。

**OpenAI 兼容**

```bash
curl http://127.0.0.1:8320/v1/chat/completions \
  -H "Authorization: Bearer sk-allproxyapi" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}'
```

**Anthropic 兼容**

```bash
curl http://127.0.0.1:8320/v1/messages \
  -H "Authorization: Bearer sk-allproxyapi" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen-max","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}'
```

暴露的路由：

| 路由 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI 兼容（含 SSE 流式） |
| `POST /v1/messages` | Anthropic Messages（含工具调用与思维链） |
| `GET /v1/models` | 上游动态发现 + 静态兜底 |
| `POST /v1/messages/count_tokens` | token 估算 |
| `GET /health` | 健康检查与账号池状态 |
| `GET /api/*` | 控制面板 API |

---

## 模型路由

三种方式，优先级从高到低：

1. **显式前缀**：`qoder/ultimate`、`antigravity/gemini-3.6-flash` —— 前缀必须是已注册的 provider id。
2. **配置规则**：`routing.model_rules` 按顺序对小写模型名做正则匹配，首个命中生效。
3. **默认 provider**：`routing.default_provider`。

未识别的 provider 前缀不会被当作路由指令，会照常走规则匹配。

---

## 凭据配置

启动时扫描 `storage.oauth_dirs` 下的 `*.json` / `*.info`。账号类型按 `type` 字段、token 前缀、或文件路径自动判别。

**Antigravity**

```json
{
  "type": "antigravity",
  "access_token": "ya29....",
  "refresh_token": "1//....",
  "project_id": "aicode-consumers",
  "email": "you@example.com"
}
```

Antigravity 的 OAuth 令牌续期需要 `client_id` / `client_secret`，这两个值**不随仓库分发**，必须填进 `data/config.yaml`：

```yaml
providers:
  antigravity:
    client_id: "<你的 Antigravity 客户端 client_id>"
    client_secret: "<对应 client_secret>"
```

从你自己已在运行的 Antigravity 客户端里取这两个值。留空时该通道仅在 access token 有效期内可用，过期后刷新会明确报错而不会静默失败。

**WorkBuddy** —— 支持 `snake_case`、`camelCase`、嵌套 `token`/`data` 三种历史落盘格式。

```json
{
  "type": "workbuddy",
  "uid": "1000xxxx",
  "realm": "cn",
  "access_token": "....",
  "refresh_token": "...."
}
```

**Qoder** —— 按 token 前缀判别：`pt-`（个人访问令牌）、`jt-`（job token）、`dt-`（设备流令牌）、`drt-`（设备刷新令牌）。

```json
{
  "type": "qoder",
  "access_token": "pt-....",
  "account_name": "you@example.com",
  "realm": "intl"
}
```

Qoder 凭据的处理规则：

| 凭据 | 行为 |
|---|---|
| `pt-` 个人令牌 | 首次使用时向 auth 主机换取 `jt-` job token |
| `dt-` 设备令牌 | 直接使用；用 `drt-` 刷新令牌续期 |
| `jt-` job token | 短生命周期，无刷新路径 —— 过期后需重新导入 |

**Qoder 未经实机验证的假设**：默认 host 对（intl `api2-v2.qoder.sh` / cn `gateway.qoder.com.cn`）与 OpenAI 兼容路由 `/model/v1/chat/completions` 来自公开的社区逆向记录，未经本仓库对真实账号的端到端验证。若上游迁移，改 `providers.qoder.base_url` / `auth_url` 即可，无需改代码。

---

## 控制面板

`http://127.0.0.1:8320/` 提供：账号池状态与冷却情况、三通道模型清单（区分实时拉取与静态兜底）、上游同步、凭据导入、冷却重置。

## 账号池

- 轮询选号，跳过冷却中与已过期的账号。
- 提交前故障转移：最多重试 3 次，每次换号。
- 429 → 冷却 60s；401/403 → 刷新后重试一次，失败则冷却 300s。
- 后台 `refresher` 每 60s 检查并续期即将过期的凭据。

---

## 开发

```bash
go build ./...
go vet ./...
go test ./...
```

---

## 凭据与责任

本仓库不包含任何凭据。`data/config.yaml`、`data/oauth/`、`*.exe` 与日志已在 `.gitignore` 中排除。请仅使用你自己合法持有的账号与密钥。
