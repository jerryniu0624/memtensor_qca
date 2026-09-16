# QCA 原生渠道（渠道类型 64）

QCA = Qoder Cloud Agent，上游地址 `https://api.qoder.com/api/v1/cloud`。
本仓库把 QCA 协议**直接实现为 new-api 的一个渠道类型**（类型号 `64`），
协议转换在网关内部完成，**不需要**再单独部署一个 OpenAI 适配服务。

---

## 1. 工作原理

一次客户端请求 = QCA 上的一个 Session（一轮 turn）：

1. `POST /sessions`，body 为 `{"agent": "<agent_id>", "environment_id": "<env_id>", "title": "..."}`
2. `GET /sessions/{id}/events/stream`（SSE）订阅事件
3. `POST /sessions/{id}/events` 投递一条 `user.message`
4. 消费 SSE 并合成 OpenAI 响应：
   - `event_delta` / `content_delta`（`type=text`）→ `chat.completion.chunk` 增量
   - `agent.message` → 兜底整段文本（增量缺失时使用）
   - `session.status_idle` 且 `stop_reason=end_turn` → `finish_reason=stop` + `usage` + `[DONE]`
   - `session.status_terminated` / `session.deleted` → 结束本轮

实现里已处理的关键约束：

- **单条用户消息**：QCA 每轮只接受一条 `user.message`，也没有 per-request system prompt。
  整份 OpenAI `messages` 由 `relay/channel/qca/prompt.go` 的 `RenderTranscript` 渲染成一条
  transcript，多消息时按 `<|system|>` / `<|user|>` / `<|assistant|>` / `<|tool|>` 分块，
  assistant 的 tool_calls 渲染成 `<tool_calls>` 块；单条 user 消息原样透传。
- **Agent 的 system 必须是 bridge-ready**：否则客户端的 system / instructions 会被当成普通数据忽略。
- **无状态**：每个请求新建 Session，多轮上下文完全靠 transcript 传递，网关不保存会话。
- **usage 是估算值**：QCA 不返回 token 用量，由 `estimateUsage` / `estimateTokens` 估算，
  适合自用统计，不适合精确对账。
- **客户端 tools 不支持**：QCA 会以 `stop_reason=requires_action` 暂停等待客户端工具结果，
  本渠道直接返回明确错误；工具请配在 Agent 侧（`agent_toolset` / `mcp_servers`）。
- **仅文本**：图片等非文本 content part 会被显式拒绝，而不是静默丢弃。
- **支持入口**：`/v1/chat/completions`、`/v1/messages`（Claude）、`/v1/responses`（Codex）、
  Gemini 格式（统一先转 OpenAI chat 再进 QCA）；不支持 embedding / rerank / audio / image。
- 上游非 2xx 响应原样透传状态码与错误体（`upstreamError`），让 new-api 的重试与自动禁用逻辑正常生效。

---

## 2. 部署过程

### 2.1 准备 QCA 侧资源

| 资源 | 形态 | 在 new-api 里的位置 |
| --- | --- | --- |
| Access Token（PAT） | `pt-...` | 渠道「密钥」 |
| Environment ID | `env_...` | 渠道 settings 的 `qca_environment_id` |
| Agent ID | `agent_...` | 渠道「模型映射」的 value，每个对外模型一个 Agent |

QCA `/models` 只暴露抽象模型 id，实测可用：
`dfmodel`(DeepSeek-Flash)、`dmodel`(DeepSeek-V4-Pro)、`kmodel_latest`(Kimi-K3)、
`gmodel`(GLM-5.3)、`qmodel`(Qwen3.7-Plus)。

创建 Agent（`POST /agents` 只需 name / model / system 三个已验证字段）：

```bash
export QCA_BASE_URL="https://api.qoder.com/api/v1/cloud"
export QCA_TOKEN="pt-xxxxxxxx"

curl -sS -X POST "$QCA_BASE_URL/agents" \
  -H "Authorization: Bearer $QCA_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"deepseek-coding-agent","model":"dfmodel","system":"<bridge system prompt>"}'
```

`system` 用 bridge 提示词（要点：把 `<|system|>` 块当作权威系统指令并完全遵循；
把 `<|user|>` / `<|assistant|>` / `<|tool|>` 当作已有对话；只回答最后一个 `<|user|>` 块；
只输出回答正文，不复述指令、不带分块标记；不透露自己是 bridge，也不用 runtime 名字自我介绍）。
完整文本见 `qca转换器v1/qca_connector/bridge.py` 的 `BRIDGE_SYSTEM_PROMPT`。

返回体里的 `id` 就是 Agent ID。已有 Agent 只想改 system 用 `PUT /agents/{id}`，
body 只发 `{"system": "..."}`；该接口是 merge，`model` / `name` / `tools` 不受影响。

> Agent 的 `name` 对模型可见，请取中性名字（例如 `deepseek-coding-agent`），
> 否则在没有 system 块时模型会用这个名字自我介绍。

### 2.2 编译 / 启动 new-api

```bash
# 后端（Go 1.25.1）
go build -o new-api .

# 前端（可选：要在 Web 控制台看到 QCA 类型时需要重新构建）
cd web && bun install && bun run build && cd ..

./new-api --port 3000
```

Docker 部署沿用官方镜像构建流程即可，不需要额外服务或额外端口。

### 2.3 在控制台创建渠道

1. 渠道 → 新建，**类型选择 `QCA`**（类型号 64）。
2. **密钥**：填 QCA 的 PAT（`pt-...`）。
3. **Base URL**：留空即用内置默认 `https://api.qoder.com/api/v1/cloud`，走内网代理时才填。
4. **QCA Environment ID**：填 `env_...`，必填，缺失时请求会直接报错。
5. **模型**：填要对外暴露的模型名，例如 `deepseek-v4-flash-0731-qca`。
   QCA 渠道不内置模型列表（`ModelList` 为空），完全由管理员声明。
6. **模型映射**：把每个对外模型名映射到它的 Agent ID —— 这是本渠道选定 Agent 的唯一方式：

```json
{
  "deepseek-v4-flash-0731-qca": "agent_00p590jinugw0oiicbl3",
  "kimi-k3-qca": "agent_00plqzsv4l8u813hwcg1",
  "glm-5.3-qca": "agent_00plqztiklm9se41xjvx",
  "deepseek-v4-pro-0813-qca": "agent_00plqzu4u7qiobzhbn9c",
  "deepseek-v4.1-flash-qca": "agent_00plqzus2quptx7nu1px",
  "qwen3.6-35b-a3b-qca": "agent_00plqzvdkwfswsqkeyuv"
}
```

渠道落库的关键字段等价于：

```json
{
  "type": 64,
  "base_url": "https://api.qoder.com/api/v1/cloud",
  "key": "pt-xxxxxxxx",
  "models": "deepseek-v4-flash-0731-qca,kimi-k3-qca,...",
  "model_mapping": "{\"deepseek-v4-flash-0731-qca\":\"agent_00p590jinugw0oiicbl3\"}",
  "settings": "{\"qca_environment_id\":\"env_00p590j3tga2p16lcsi3\"}"
}
```

其中 settings 对应 `relaykit/dto/channel_settings.go` 里的 `ChannelOtherSettings.QCAEnvironmentID`
（JSON 字段名 `qca_environment_id`）。

> **计费**：这些模型名默认没有价格。要么在「模型价格」里补价，要么开启自用模式
> （SelfUseMode），否则请求会被计价校验拦下。

### 2.4 验收

```bash
export KEY="sk-你的令牌"
export API="http://127.0.0.1:3000"

# OpenAI 协议（非流式）
curl -sS "$API/v1/chat/completions" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","messages":[{"role":"user","content":"只回复两个字：在的"}]}'

# OpenAI 协议（流式），期望帧序 role -> content... -> finish_reason=stop -> usage -> [DONE]
curl -sSN "$API/v1/chat/completions" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","stream":true,"messages":[{"role":"user","content":"只回复两个字：在的"}]}'

# Claude 协议
curl -sS "$API/v1/messages" -H "x-api-key: $KEY" -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"kimi-k3-qca","max_tokens":512,"messages":[{"role":"user","content":"只回复两个字：在的"}]}'

# Responses（Codex）协议
curl -sS "$API/v1/responses" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","input":"只回复两个字：在的"}'
```

### 2.5 实测结果

同一实例、同一批模型名，基线渠道（类型 1 → 上游网关）与 QCA 渠道（类型 64）对比：

| 模型 | 基线渠道 | QCA 渠道 |
| --- | --- | --- |
| `deepseek-v4-flash-0731` | OK，回复「在的」，finish=stop | OK，回复「在的」，finish=stop |
| `qwen3.6-35b-a3b` | OK，回复「在的」，finish=stop | OK（Agent 用 `qmodel`，见下方说明） |
| `kimi-k3` | **HTTP 503**，上游 `model_not_found`，直连上游同样 503 | OK，回复「在的」，finish=stop |
| `glm-5.3` | OK，回复「在的」，finish=stop | OK，回复「在的」，finish=stop |
| `deepseek-v4-pro-0813` | OK，回复「在的」，finish=stop | OK，回复「在的」，finish=stop |
| `deepseek-v4.1-flash` | OK，回复「在的」，finish=stop | OK，回复「在的」，finish=stop |

QCA 渠道额外验证：流式帧序正确；`/v1/messages` 返回 200 且 SSE 为
`message_start…message_stop`；`/v1/responses` 返回 200 且 SSE 为
`response.created…response.completed`。

说明与已知差异：

- **`qwen3.6-35b-a3b` 是近似映射**：QCA 侧没有 qwen3.6，对应 Agent 用的是 `qmodel`
  （Qwen3.7-Plus），只作链路可用性验证，不等于同一模型。
- **`kimi-k3` 基线 503 是上游故障**：绕过 new-api 直连上游网关同样返回 503，与本渠道无关。
- QCA 侧 `usage` 为估算值，token 数不能与基线直接比较。
- QCA 每轮新建 Session，首字节延迟通常高于普通推理接口（实测 1.4–10.3s）。

---

## 3. new-api 的修改清单

### 3.1 新增：`relay/channel/qca/`

| 文件 | 作用 |
| --- | --- |
| `constants.go` | 渠道名 `QCA`、默认 Base URL、SSE 事件与停止原因常量、transcript 前导语、`requires_action` 错误文案；`ModelList` 故意为空 |
| `dto.go` | 内部 hand-off 结构 `ChatRequest`；QCA 的 `POST /sessions`、`POST /sessions/{id}/events` 请求体与事件解码结构（兼容 `data` 包装） |
| `prompt.go` | `RenderTranscript`：OpenAI messages → QCA 单条 user.message；tool_calls 渲染；非文本 part 拒绝 |
| `turn.go` | 核心逻辑：`startTurn`（建会话 → 开流 → 投递消息）、`consumeTurn`（SSE 解析与轮次结束判定 `idleStopReason`）、合成 `chat.completion` / `chat.completion.chunk`、`estimateUsage`、`upstreamError` 透传上游状态码 |
| `adaptor.go` | 实现 `channel.Adaptor`：请求 URL、请求头（`Authorization: Bearer <PAT>`）、各协议 Convert（Claude / Gemini / Responses 先转 OpenAI chat）、`DoRequest`、`DoResponse`（复用 openai 的 handler 输出） |
| `responses.go` | `/v1/responses` 的非流式与流式输出合成 |
| `adaptor_test.go` | 表驱动单测 + `httptest` 假 QCA 服务的端到端用例 |

### 3.2 修改：注册渠道类型（后端）

| 文件 | 改动 |
| --- | --- |
| `constant/channel.go` | 新增 `ChannelTypeQCA = 64`、`ChannelBaseURLs[64]`、`ChannelTypeNames[64] = "QCA"` |
| `constant/api_type.go` | 新增 `APITypeQCA`（放在 `APITypeDummy` 之前） |
| `common/api_type.go` | `ChannelType2APIType` 增加 `ChannelTypeQCA` → `APITypeQCA` |
| `relay/relay_adaptor.go` | `GetAdaptor` 增加 `case constant.APITypeQCA: return &qca.Adaptor{}` |
| `relaykit/dto/channel_settings.go` | `ChannelOtherSettings` 增加 `QCAEnvironmentID`（JSON `qca_environment_id,omitempty`） |

> 未加入 `streamSupportedChannels`：QCA 上游不接受 `stream_options`，
> 流式 `usage` 由本渠道自行合成。

### 3.3 修改：Web 控制台（前端）

| 文件 | 改动 |
| --- | --- |
| `web/src/features/channels/constants.ts` | `CHANNEL_TYPE_QCA = 64`、类型名 `QCA`、provider 描述、密钥占位提示 |
| `web/src/features/channels/lib/channel-utils.ts` | 渠道图标映射增加 `64` |
| `web/src/features/channels/lib/channel-form.ts` | 表单字段 `qca_environment_id`：schema、默认值、回填、写入 settings JSON |
| `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx` | QCA 类型的 Environment ID 输入框，并纳入敏感字段校验 |
| `web/src/i18n/static-keys.ts`、`locales/en.json`、`locales/zh.json` | 新增文案的 i18n key；其余语言回退英文，可用 `bun run i18n:sync` 补齐 |

### 3.4 验证命令

```bash
gofmt -l relay/channel/qca                 # 无输出
go build ./...                             # 通过
go test ./relay/channel/qca/...            # ok
cd relaykit && GOWORK=off go build ./...   # relaykit 模块独立性校验，通过
```
