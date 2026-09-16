# my_agent

基于 [cloudwego/eino](https://github.com/cloudwego/eino) 的**多模态多 Agent 系统**后端。

V1：单 Agent + 全量多模态输入（文本/图片/音频/视频/文件，URL 或 Base64）+ 内存多轮会话 + 同步/SSE 流式双模式。

## 快速开始

```bash
export OPENAI_API_KEY=sk-...            # 或任何 OpenAI 兼容服务
export OPENAI_BASE_URL=https://api.openai.com/v1
export OPENAI_MODEL=gpt-4o-mini

go run ./cmd/my_agent -config configs/config.yaml
# → my_agent listening on :8080 (agents: [main_agent])
```

接入其他 OpenAI 兼容服务只需换环境变量：

| 服务 | OPENAI_BASE_URL |
|---|---|
| DeepSeek | `https://api.deepseek.com/v1` |
| Qwen | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| vLLM（本地） | `http://localhost:8000/v1` |

## API

| Method | Path | 说明 |
|---|---|---|
| POST | `/v1/chat/completions` | 对话；`"stream": true` 切换 SSE |
| GET | `/v1/sessions/:id` | 会话历史回显 |
| DELETE | `/v1/sessions/:id` | 清除会话（204） |
| GET | `/v1/agents` | 已注册 agent 列表 |
| GET | `/healthz` | 健康检查 |

### 请求

```jsonc
{
  "session_id": "s1",          // 可选；缺省自动生成 UUID
  "agent": "main_agent",       // 可选；缺省用配置里的默认 agent
  "message": "你好",            // 纯文本便捷字段；或用 parts（多模态）
  "parts": [                    // message 与 parts 二选一，parts 优先
    {"type": "text", "text": "描述这张图"},
    {"type": "image_url", "url": "https://example.com/cat.jpg", "detail": "high"},
    {"type": "audio_url", "base64_data": "...", "mime_type": "audio/wav"},
    {"type": "video_url", "url": "https://example.com/clip.mp4"},
    {"type": "file_url", "url": "https://example.com/doc.pdf", "name": "doc.pdf"}
  ],
  "stream": false               // true → SSE 流式响应
}
```

Part 规则：媒体 part 的 `url` 与 `base64_data` 二选一；`base64_data` 必须带 `mime_type`（裸 base64，无 `data:` 前缀）；`detail` 仅图片可用（high/low/auto）。

### 响应（非流式）

```json
{
  "session_id": "s1",
  "agent": "main_agent",
  "role": "assistant",
  "content": "The image shows a tabby cat...",
  "finish_reason": "stop",
  "usage": {"prompt_tokens": 812, "completion_tokens": 44, "total_tokens": 856},
  "created_at": 1760000000
}
```

### 响应（SSE，`stream: true`）

```
data: {"type":"chunk","agent":"main_agent","delta":"The image"}

data: {"type":"chunk","agent":"main_agent","delta":" shows a tabby cat..."}

data: {"type":"done","session_id":"s1","agent":"main_agent","finish_reason":"stop","usage":{...}}
```

### 错误约定

```json
{"error": {"code": "invalid_request", "message": "..."}}
```

| HTTP | code | 场景 |
|---|---|---|
| 400 | `invalid_request` | 参数校验失败 |
| 404 | `session_not_found` / `agent_not_found` | 查找缺失 |
| 413 | `request_too_large` | 请求体超限（默认 32 MiB） |
| 502 | `upstream_error` | 模型/供应商调用失败 |
| 500 | `internal_error` | 其他 |

SSE 已发送响应头后出错，降级为 `data: {"type":"error","message":"..."}` 事件。

## 多模态支持矩阵（OpenAI 兼容适配器）

| Part 类型 | URL | Base64 | 备注 |
|---|---|---|---|
| text | — | — | 始终支持 |
| image_url | ✅ | ✅（需 mime） | detail 可选 |
| audio_url | ❌（适配器拒绝 URL） | ✅（mime 须在映射表内：audio/wav、audio/mpeg…） | |
| video_url | ✅ | ✅（需 mime） | |
| file_url | 透传 | 透传 | OpenAI 适配器当前不支持 file part（报 502）；eino schema 已支持，其他适配器（如 ark）可用 |

## 架构

```
cmd/my_agent          # 装配：config → llm → agent → session → chat → api
internal/
  config/             # yaml + ${ENV} 展开 + 校验
  llm/                # 模型工厂注册表（provider.go + openai.go）
  agent/              # agent 注册表 + 构建（registry.go + build.go）
  session/            # Store 接口 + 内存实现（memory/）
  chat/               # 编排核心：Complete / Stream / 会话持久化
  api/                # DTO / handlers / router / 错误约定
```

依赖严格单向：`api → chat → {session, agent} → {llm, config}`。

**三个扩展接缝**（各一个接口，换实现不动其他层）：

| 接缝 | 接口 | 扩展场景 |
|---|---|---|
| 模型供应商 | `llm.Registry`（Factory 按 type 注册） | 加 ark/ollama/gemini = 加一个工厂文件 |
| Agent | `agent.Registry` + `agent.Runner` | 加工具/多 Agent 编排只改 `agent/build.go` |
| 会话存储 | `session.Store`（3 方法） | memory → Redis，配置切 `sessions.store` |

### 配置（configs/config.yaml）

```yaml
models:            # 命名模型实例，agent 按 key 引用；type 路由到 llm 工厂
  main: { type: openai, api_key: ${OPENAI_API_KEY}, base_url: ..., model: ... }
agents:
  default: main_agent
  definitions:     # yaml key 即 agent 名；加 agent 只加配置
    main_agent: { model: main, instruction: ..., max_iterations: 20 }
sessions: { store: memory, max_messages: 100 }
```

## 演进路线（架构已预留）

- **V2 工具**：`internal/tools/registry.go`（同 llm 工厂模式）+ `AgentConfig.Tools` + `build.go` 填 `adk.ToolsConfig`，HTTP/service 层零改动
- **V3 多 Agent**：加 agent 纯配置化；子 agent 用 `adk.NewAgentTool` 包成工具挂给主管 agent（eino 官方推荐），改 `agents.default` 切换入口
- **V4 持久会话**：`internal/session/redis` 实现 `session.Store`
- **后续**：RAG（即一种工具）、认证/限流（gin 中间件）、观测（eino callbacks 注入 `build.go`）

## 运维限制（V1 已知约束）

### 单进程部署

会话和上传元数据都存在**进程内存**里：

- `internal/session/memory`：所有 session + 消息
- `internal/uploads/memory`：所有 upload 元数据 + session 倒排索引
- **进程重启即全部丢失**，OSS 对象本身残留（依赖 TOS lifecycle 兜底清理）

**多实例部署会导致**：
- 不同实例看到不同的 session 列表（A 实例建的 session，B 实例查不到）
- `X-Session-Id` 校验在不同实例上表现不一致
- SSE 长连接被负载均衡切断（需要在 LB 层做 IP 亲和）

V4 接 Redis session store 时这些问题自动消失。

### CORS 默认未配置

服务启动后**默认不允许跨域请求**（gin 默认不带 CORS middleware）。

前端如果跟后端不同源，必须自行加一层 nginx 反代，或在 `cmd/my_agent/main.go` 的 `NewRouter` 里加 CORS middleware（仅建议私部署/开发环境用，**生产环境走反代**）。

### 密钥管理

- `configs/local.yaml` 已在 `.gitignore`，放真实 key
- 进程不监听 secret manager，**改 yaml 必须重启**
- 多个 chat / image / video model 共享同一对方舟 AK/SK（细粒度鉴权隔离要靠 IAM 控制台配子账号）

### 配置加载顺序（无 `-config` 参数时）

1. `configs/local.yaml`（本地覆盖，gitignored）
2. `configs/config.yaml`（默认模板，提交进 git）

### Upload 流式配额

- 单文件 ≤ `uploads.max_size`（默认 10 MiB）
- **没有 in-flight 并发限流**：恶意并发多文件上传可打爆进程内存（同时占用 32 MiB 上限 + 文件内容）。建议前面加 nginx 限流。

## 开发


```bash
make build      # go build ./...
make test       # go test ./...
make test-race  # 竞态检测
make vet        # 静态检查
make run        # 启动服务
```

依赖：Go 1.23+，eino v0.9.x，eino-ext openai 适配器，gin。
