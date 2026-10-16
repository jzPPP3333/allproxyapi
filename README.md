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
    checkin.go              签到编排
    checkin_schedule.go     每日签到调度器
    translator.go           OpenAI ⇄ Gemini ⇄ Anthropic 协议转换
    schema.go               Gemini 工具参数 schema 净化
    openai/, anthropic/     下游协议类型定义
  egress/
    antigravity/            Cloud Code PA 客户端 + envelope 解包 + project 嗅探 + 模型目录
    workbuddy/              双 region 客户端 + 身份指纹头 + 模型目录 + 签到
    qoder/                  region 端点 + 凭据交换/刷新 + COSY 信封 + 设备指纹 + 签到
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
    ModelCatalog() []pool.ModelMetadata                         // 同一份目录，带元数据
    FetchModels(ctx, *pool.Account) ([]pool.ModelMetadata, error)
    Chat(ctx, *pool.Account, *openai.ChatCompletionRequest, rawBody []byte) (*http.Response, error)
    Unwrap(io.Reader) (io.Reader, error)
    Checkin(ctx, *pool.Account) (pool.CheckinResult, error)     // 无签到概念时返回 unsupported
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
| `GET /api/status` | 面板状态 |
| `POST /api/checkin` | 立即签到（可带 `provider` / `id` 过滤） |
| `GET /api/checkin` | 上次调度结果与各通道签到支持情况 |
| `GET /api/*` | 其余控制面板 API |

---

## 模型路由

三种方式，优先级从高到低：

1. **显式前缀**：`qoder/ultimate`、`antigravity/gemini-3.6-flash` —— 前缀必须是已注册的 provider id。
2. **配置规则**：`routing.model_rules` 按顺序对小写模型名做正则匹配，首个命中生效。
3. **默认 provider**：`routing.default_provider`。

未识别的 provider 前缀不会被当作路由指令，会照常走规则匹配。

### 清单里的名字可以直接用

`/v1/models` 列出的每个模型名都保证能**不加前缀**直接请求，并且落到列它出来的那个 provider。这条不变量有测试守着（`internal/ingress/models_test.go`），它覆盖了曾经的三个真实缺陷：

| 症状 | 原因 |
|---|---|
| 只配了一个 provider，清单里只剩它 | 静态兜底只在动态模型为空时执行，一个账号同步成功就会盖住另外两家 |
| 选了清单里的名字，拿到的却是另一个模型 | 路由规则写死了 `upstream_model`，把所有 `claude-*` 折叠成同一个上游模型 |
| 同一个模型名出现两次、归属不同 | 去重的 key 只用模型名，没用 `provider/模型名` |

因此 `model_rules` 里的 `upstream_model` 留空即可 —— provider 自己的别名表负责映射，在规则里再写一次反而会覆盖掉它。

**多 provider 提供同名模型时**：清单里会各列一行，归属于各自的 provider。此时不加前缀会有歧义，走规则匹配；要精确指定请用 `provider/模型名`。

**人话别名仍然可用**：`claude-3-7-sonnet`、`deepseek-chat`、`kimi` 这类名字照常接受并被 provider 的映射表转换，只是不再作为独立模型出现在清单里 —— 两个名字指向同一个上游模型不该被算成两个模型。

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

### Qoder COSY 信封

Qoder 客户端可以给请求套一层加密信封。网关内置了完整的纯 Go 实现（RSA 包裹会话密钥、AES-128-CBC 载荷、自定义 base64 字典、HMAC 签名），默认**关闭**——公共推理主机接受明文 JSON。

```

        明文 JSON ──► AES-128-CBC（随机 session key + IV）──────────┐
                          │                                        │
                          └── session key ──► RSA 公钥包裹 ──► "ek" │
                                                                    ▼
        字典编码( JSON{v, alg, key_alg, ek, iv, data, ts, nonce, sig} )
```

启用时把密钥填进 `data/config.yaml`：

```yaml
providers:
  qoder:
    cosy:
      public_key_pem: |
        -----BEGIN PUBLIC KEY-----
        ...
        -----END PUBLIC KEY-----
      key_alg: "RSA-OAEP-SHA256"   # 或 RSA-PKCS1v15
      sign_key: "<可选 HMAC 密钥>"
```

配置项含义：

| 字段 | 说明 |
|---|---|
| `public_key_pem` | 包裹会话密钥的服务器 RSA 公钥，留空则关闭信封 |
| `private_key_pem` | 仅在上游回包同样加密时才需要 |
| `key_alg` | RSA 填充模式，默认 OAEP-SHA256 |
| `alphabet` | 覆盖默认的 64 字符 base64 字典 |
| `sign_key` | 非空则对 `iv‖密文‖时间戳` 做 HMAC-SHA256 |

密钥解析失败时信封会保持关闭并在日志中报错，不会静默发送上游无法解读的请求。

设备指纹（`X-Machine-Id` / `X-Machine-Token`）由账号身份派生并持久化，而非每次启动随机生成——随机值会让上游把同一个账号当成一台新设备，从而触发风控。派生规则见 [fingerprint.go](internal/egress/qoder/fingerprint.go)。

### 项目 ID 自动嗅探

Antigravity 的信封需要一个 project id。账号文件里没有时，网关会调用 `v1internal:loadCodeAssist` 自动探测，缓存后写回账号文件；探测失败则回退到 `providers.antigravity.default_project`，不影响正常请求。

### 每日签到

签到路由因部署而异，因此**不硬编码**，而是按 provider 配置。未配置时该通道报告为 `unsupported`，不会拿探测到的 URL 去打真实账号。

```yaml
providers:
  workbuddy:
    checkin_path: "/v2/<你的签到路由>"
    daily_checkin: true
    checkin_hour: 9
```

手动触发或查看上次结果：

```bash
curl http://127.0.0.1:8320/api/checkin                                   # 上次调度结果 + 各通道支持情况
curl -X POST http://127.0.0.1:8320/api/checkin                           # 立即为所有通道签到
curl -X POST "http://127.0.0.1:8320/api/checkin?provider=qoder"          # 指定通道
curl -X POST "http://127.0.0.1:8320/api/checkin?provider=qoder&id=<acc>" # 指定账号
```

Antigravity 的额度随 Google 套餐发放，没有客户端侧签到动作，固定报告 `unsupported`。

### 额度耗尽冷却

额度耗尽与限流性质不同：前者不会在短时间内恢复。因此响应 `402`，或响应体携带额度耗尽码（Qoder/WorkBuddy `6004`/`6005`/`10006`，Gemini `RESOURCE_EXHAUSTED`）时，账号会被冷却 **30 分钟**，而不是限流的 60 秒，让账号池直接绕开它而不是反复撞同一堵墙。

---

## 控制面板

`http://127.0.0.1:8320/` 提供：账号池状态与冷却情况、三通道模型清单（区分实时拉取与静态兜底）、上游同步、凭据导入、冷却重置。

## 账号池

- 轮询选号，跳过冷却中与已过期的账号。
- 提交前故障转移：最多重试 3 次，每次换号。
- 429 → 冷却 60s；401/403 → 刷新后重试一次，失败则冷却 300s。
- 后台 `refresher` 每 60s 检查并续期即将过期的凭据。

---

## 构建与部署

**本地构建**

```bash
go build -o allproxyapi ./cmd/proxy
go build ./... && go vet ./... && go test ./...
```

**跨平台打包** —— 一次产出 6 个目标，每个都带示例配置与许可证：

```bash
./build.sh                    # 全部目标
./build.sh linux/amd64        # 指定目标
```

产物在 `dist/`，含 `windows/amd64`、`windows/arm64`、`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`。二进制内嵌版本信息：

```bash
allproxyapi -version
# AllProxyAPI v1.0.0 (commit a1b2c3d, built 2026-10-16T08:50:22Z)
```

**Docker**

```bash
docker compose up -d --build
```

镜像基于 `alpine:3.20`，多阶段构建，以非 root 用户运行，并带 `/health` HEALTHCHECK。`docker-compose.yml` 默认只把端口发布到宿主机回环（`127.0.0.1:8320`）——网关自身有 API key 鉴权，但把一个 LLM 代理默认暴露到网络上不是安全的默认值。凭据通过 `./data` 卷持久化，令牌轮换后写回宿主机。

---

## 开发

```bash
go build ./...
go vet ./...
go test ./...
node webui-test/dashboard.test.js   # 面板渲染测试（需 Node）
```

面板是内嵌 HTML 里的手写 JS，没有构建步骤，所以拼写错误和过期的厂商假设 `go test` 看不到。`webui-test/dashboard.test.js` 把内嵌脚本抽出来，用最小 DOM 桩跑一遍渲染路径 —— 新增 provider 时尤其跑一下，否则很容易出现模型挂错厂商标签这类问题。

---

## 凭据与责任

本仓库不包含任何凭据。`data/config.yaml`、`data/oauth/`、`*.exe` 与日志已在 `.gitignore` 中排除。请仅使用你自己合法持有的账号与密钥。
