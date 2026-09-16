# QCA 渠道 API 调用手册

面向**调用方**：管理员已按 `QCA_CHANNEL.md` 建好 QCA 渠道（渠道类型 64）之后，
客户端如何用标准 API 调用它。部署与上线措施请看 `QCA_DEPLOYMENT.md`，
协议原理与代码改动清单请看 `QCA_CHANNEL.md`。

下面所有响应都是**实测抓包**（new-api 实例 + QCA 渠道，模型 `*-qca`），
替换 Base URL / 令牌 / 模型名即可复现。

---

## 1. 前置信息

| 项 | 值 |
| --- | --- |
| Base URL | 你的 new-api 地址，例如 `https://your-host`（本机默认 `http://127.0.0.1:3000`） |
| 令牌 | new-api 控制台签发的 `sk-...`（「令牌」页面），**不是** QCA 的 `pt-...` |
| 模型名 | 渠道里声明且绑定了 Agent 的名字，例如 `glm-5.3-qca` |

QCA 的 PAT、Environment ID、Agent ID 全部配在渠道里，客户端不需要也拿不到；
请求里只出现模型名，响应里的 `model` 字段也回显这个名字（不会泄漏 agent id）。

查当前令牌可用的模型：

```bash
curl -s https://your-host/v1/models -H "Authorization: Bearer sk-你的令牌"
```

实测返回的模型 id（本例渠道）：

```text
deepseek-v4-flash-0731-qca   deepseek-v4-pro-0813-qca   deepseek-v4.1-flash-qca
glm-5.3-qca                  kimi-k3-qca                qwen3.6-35b-a3b-qca
```

---

## 2. 四种入口协议

| 入口 | 协议 | 鉴权头 | 实测 |
| --- | --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat | `Authorization: Bearer sk-...` | 200 |
| `POST /v1/messages` | Anthropic Messages | `x-api-key: sk-...` + `anthropic-version: 2023-06-01` | 200 |
| `POST /v1/responses` | OpenAI Responses（Codex） | `Authorization: Bearer sk-...` | 200 |
| `POST /v1beta/models/{model}:generateContent` | Gemini | `x-goog-api-key: sk-...` | 200 |

四个入口最终走同一条 QCA 链路：非 OpenAI 协议先被转成 OpenAI chat 请求，
再渲染成 QCA 的一轮 turn，返回时按客户端协议重新组装。

---

## 3. OpenAI Chat Completions

### 3.1 非流式

```bash
curl -s https://your-host/v1/chat/completions \
  -H "Authorization: Bearer sk-你的令牌" \
  -H "Content-Type: application/json" \
  -d '{
        "model": "glm-5.3-qca",
        "messages": [
          {"role": "system", "content": "你是一个只说两个字的助手。"},
          {"role": "user", "content": "只回复两个字：在的"}
        ]
      }'
```

实测响应（`usage` 的明细字段已折叠）：

```json
{
  "id": "chatcmpl-202609160844552689610008268d9d6nvncN3hj",
  "model": "glm-5.3-qca",
  "object": "chat.completion",
  "created": 1789548298,
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "在的" },
      "finish_reason": "stop"
    }
  ],
  "usage": { "prompt_tokens": 39, "completion_tokens": 1, "total_tokens": 40 }
}
```

### 3.2 流式

```bash
curl -sN https://your-host/v1/chat/completions \
  -H "Authorization: Bearer sk-你的令牌" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash-0731-qca","stream":true,"messages":[{"role":"user","content":"只回复两个字：在的"}]}'
```

实测帧序（固定）：

```text
delta.role=assistant -> delta.content... -> finish_reason=stop -> usage -> [DONE]
```

首帧与内容帧（实测原文）：

```text
data: {"id":"chatcmpl-20260916084516...","object":"chat.completion.chunk","created":1789548316,"model":"deepseek-v4-flash-0731-qca","choices":[{"delta":{"role":"assistant"},"logprobs":null,"finish_reason":null,"index":0}],"usage":null}

data: {"id":"chatcmpl-20260916084516...","object":"chat.completion.chunk","created":1789548316,"model":"deepseek-v4-flash-0731-qca","choices":[{"delta":{"content":"在"},"logprobs":null,"finish_reason":null,"index":0}],"usage":null}
```

结束帧（实测原文，明细字段已折叠）：

```text
data: {...,"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}

data: [DONE]
```

说明：QCA 渠道**总是**在结束前补一帧 `usage`，不需要传 `stream_options.include_usage`
（传了也不影响，`stream_options` 本身被忽略）。

### 3.3 Python（openai SDK）

```python
from openai import OpenAI

client = OpenAI(api_key="sk-你的令牌", base_url="https://your-host/v1")

resp = client.chat.completions.create(
    model="glm-5.3-qca",
    messages=[
        {"role": "system", "content": "你是一个只说两个字的助手。"},
        {"role": "user", "content": "只回复两个字：在的"},
    ],
)
print(resp.choices[0].message.content, resp.choices[0].finish_reason)
print(resp.usage.prompt_tokens, resp.usage.completion_tokens)

stream = client.chat.completions.create(
    model="glm-5.3-qca",
    messages=[{"role": "user", "content": "只回复两个字：在的"}],
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

### 3.4 Node（fetch）

```js
const res = await fetch('https://your-host/v1/chat/completions', {
  method: 'POST',
  headers: {
    'Authorization': `Bearer ${process.env.NEW_API_KEY}`,
    'Content-Type': 'application/json',
  },
  body: JSON.stringify({
    model: 'glm-5.3-qca',
    stream: true,
    messages: [{ role: 'user', content: '只回复两个字：在的' }],
  }),
})

const reader = res.body.getReader()
const decoder = new TextDecoder()
while (true) {
  const { value, done } = await reader.read()
  if (done) break
  for (const line of decoder.decode(value).split('\n')) {
    if (!line.startsWith('data: ')) continue
    const payload = line.slice(6)
    if (payload === '[DONE]') return
    const delta = JSON.parse(payload).choices?.[0]?.delta?.content
    if (delta) process.stdout.write(delta)
  }
}
```

### 3.5 请求字段支持矩阵

| 字段 | 行为 |
| --- | --- |
| `messages` | 支持；整份列表渲染成 QCA 一轮 turn 的单条 user.message（见 §4） |
| `stream` | 支持；帧序固定，见 §3.2 |
| `role: system` / `developer` | 支持；渲染成 transcript 的 `<|system|>` 块（需 Agent 是 bridge-ready） |
| `role: tool`、`assistant.tool_calls` | 支持；作为文本渲染进 transcript，可用于回放含工具调用的历史 |
| `tools` / `tool_choice` | **不会真正执行**：不下发给 QCA。默认放行并丢弃；渠道若配了 `tool_loss_policy: strict` 则直接拒绝 |
| `temperature` / `top_p` / `max_tokens` / `n` / `stop` / `seed` / `frequency_penalty` / `presence_penalty` / `logprobs` / `response_format` | **忽略**：QCA 的一轮 turn 不接受采样参数，行为由 Agent 自身配置决定 |
| `stream_options` | 忽略（`usage` 恒定返回） |
| 图片 / 音频 / 文件等非文本 content part | **显式报错拒绝**，不会静默丢弃 |
| 其它字段（`user`、`metadata` 等） | 忽略 |

> 重点：`max_tokens` 不生效，QCA 输出长度不受客户端限制；要约束输出请写进提示词，
> 或在 QCA Agent 上配置。

---

## 4. 多轮对话（必读）

- 网关对 QCA 是**无状态**的：每个请求新建一个 QCA Session，用完即弃，不记忆上一轮。
  多轮对话必须**每次带上完整 messages**（含 system 与全部历史）。
- transcript 渲染规则（`relay/channel/qca/prompt.go`）：
  - 只有一条 user 消息时：原样作为 QCA 的 user.message，不加任何包装。
  - 多条消息时：先加一句英文前导语，再按 `<|system|>` / `<|user|>` / `<|assistant|>` / `<|tool|>` 分块。
  - assistant 的 tool_calls 渲染成 `<tool_calls>name(arguments)</tool_calls>` 文本块。
  - 最后一条必须是 `user`（回填工具结果时可以是 `tool`），否则直接报错。
- 实测多轮（`kimi-k3-qca`）：

```bash
curl -s https://your-host/v1/chat/completions \
  -H "Authorization: Bearer sk-你的令牌" -H "Content-Type: application/json" \
  -d '{"model":"kimi-k3-qca","messages":[
        {"role":"system","content":"你只用中文回答，且不超过10个字。"},
        {"role":"user","content":"我叫小明。"},
        {"role":"assistant","content":"你好小明"},
        {"role":"user","content":"我叫什么？"}]}'
```

实测回复 `小明`，`finish_reason=stop`，`usage` 48/1 —— 说明 system 与历史都被正确传递。

- 客户端 system 是否生效，取决于 Agent 的 server-side system 是否为 bridge-ready
  （见 `QCA_CHANNEL.md` §2.1）。若不是，system 会被当成普通数据而忽略。

---

## 5. Anthropic Messages 协议

```bash
curl -s https://your-host/v1/messages \
  -H "x-api-key: sk-你的令牌" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
        "model": "kimi-k3-qca",
        "max_tokens": 512,
        "system": "你是一个只说两个字的助手。",
        "messages": [{"role": "user", "content": "只回复两个字：在的"}]
      }'
```

实测响应（`usage.billing_usage` 等网关附加字段已折叠）：

```json
{
  "id": "chatcmpl-202609160845392161800008268d9d6UNTR0mfb",
  "type": "message",
  "role": "assistant",
  "content": [{ "type": "text", "text": "在的" }],
  "stop_reason": "end_turn",
  "model": "kimi-k3-qca",
  "usage": {
    "input_tokens": 39,
    "output_tokens": 1,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  }
}
```

流式（`"stream": true`）实测事件序列：

```text
message_start -> content_block_start -> content_block_delta
              -> content_block_stop  -> message_delta -> message_stop
```

Python（anthropic SDK）：

```python
from anthropic import Anthropic

client = Anthropic(api_key="sk-你的令牌", base_url="https://your-host")
msg = client.messages.create(
    model="kimi-k3-qca",
    max_tokens=512,
    system="你是一个只说两个字的助手。",
    messages=[{"role": "user", "content": "只回复两个字：在的"}],
)
print(msg.content[0].text, msg.stop_reason)
```

Claude Code 接入：

```bash
export ANTHROPIC_BASE_URL="https://your-host"
export ANTHROPIC_AUTH_TOKEN="sk-你的令牌"
export ANTHROPIC_MODEL="kimi-k3-qca"
```

---

## 6. OpenAI Responses 协议（Codex）

```bash
curl -s https://your-host/v1/responses \
  -H "Authorization: Bearer sk-你的令牌" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","input":"只回复两个字：在的"}'
```

实测关键字段：

```text
id      = chatcmpl-202609160845439841300008268d9d6ve1hNopx
object  = response          status = completed        model = glm-5.3-qca
output  = [{ type: message, role: assistant, content: [{ type: output_text, text: "在的" }] }]
usage   = { prompt_tokens: 3, completion_tokens: 1, total_tokens: 4 }
```

流式（`"stream": true`）实测事件序列：

```text
response.created -> response.output_item.added -> response.output_text.delta
                 -> response.output_text.done  -> response.output_item.done
                 -> response.completed
```

Codex CLI 配置（`~/.codex/config.toml`）：

```toml
model = "glm-5.3-qca"
model_provider = "memtensor_qca"

[model_providers.memtensor_qca]
name = "memtensor-qca"
base_url = "https://your-host/v1"
env_key = "MEMTENSOR_QCA_API_KEY"
wire_api = "responses"
```

---

## 7. Gemini 协议

```bash
curl -s -X POST "https://your-host/v1beta/models/glm-5.3-qca:generateContent" \
  -H "x-goog-api-key: sk-你的令牌" -H "Content-Type: application/json" \
  -d '{"contents":[{"role":"user","parts":[{"text":"只回复两个字：在的"}]}]}'
```

实测响应（`billing_usage` 已折叠）：

```json
{
  "candidates": [
    {
      "content": { "role": "model", "parts": [{ "text": "在的" }] },
      "finishReason": "STOP",
      "index": 0,
      "safetyRatings": []
    }
  ],
  "usageMetadata": {
    "promptTokenCount": 3,
    "candidatesTokenCount": 1,
    "totalTokenCount": 4
  }
}
```

---

## 8. usage 与计费

- QCA 不返回 token 用量，网关按文本**估算**（`estimateUsage` / `estimateTokens`）。
  同一句提示词，QCA 渠道估算 `prompt_tokens=3`，而基线渠道由上游真实计数得到 `89`
  —— 估算值只适合自用统计，**不能用于对账**。
- 四种协议都会带 usage：OpenAI 在 `usage`；Claude 在 `usage.input_tokens/output_tokens`；
  Responses 在 `response.usage`；Gemini 在 `usageMetadata`。
- new-api 按「模型价格」中该模型名的单价 × 估算 token 计费；模型没配价时需开启自用模式
  （SelfUseMode），否则请求会被计价校验拦下。

---

## 9. 错误与排查

报错文本为网关原样返回，可直接搜索定位：

| 报错 | 原因 | 处理 |
| --- | --- | --- |
| `model "xxx" is not bound to a QCA Agent: add it to the channel model mapping` | 渠道模型映射里没有这个模型 | 控制台在「模型映射」加 `"xxx": "agent_..."` |
| `the QCA channel is missing qca_environment_id in its settings` | 渠道 settings 没填环境 ID | 控制台补 QCA Environment ID（`env_...`） |
| `'messages' must be a non-empty array` | messages 为空 | 至少给一条 user 消息 |
| `the last message must have role 'user', or role 'tool' when returning tool results` | 最后一条不是 user/tool | 调整消息顺序 |
| `messages[i].role "xxx" is not supported by the QCA channel` | 角色非法 | 仅支持 system/developer/user/assistant/tool |
| `messages[i].content contains a "image_url" part; the QCA channel relays text only` | 传了图片/音频等非文本 | 本渠道只支持纯文本 |
| `QCA paused the turn waiting for a client-side tool result (stop_reason=requires_action)...` | Agent 侧配了需客户端执行的工具 | 改用 Agent 侧工具（`agent_toolset` / `mcp_servers`） |
| `QCA session reached terminal state "..." before the turn completed` | 上游提前终止会话（超时/风控/环境异常） | 重试；检查 Environment 状态 |
| `QCA ended the session without completing the turn` / `QCA ended the turn without completing it (stop_reason=...)` | 同上 | 重试并查看渠道日志 |
| `QCA event stream ended before the turn completed` | SSE 被中断（网络或反向代理缓冲） | 反代关闭 `proxy_buffering`，放大读超时 |
| `QCA returned an invalid JSON response` / `QCA session response did not include an id` | 上游返回异常 | 检查 PAT 是否有效、Base URL 是否正确 |
| HTTP 401（上游原样透传） | PAT 失效或无权限 | 换 PAT；注意 401 可能触发 new-api 自动禁用渠道 |
| HTTP 503 / `model_not_found` | 上游服务不可用 | 与本渠道无关，等上游恢复或换 Agent 模型 |

延迟提示：QCA 每轮都要新建 Session，首字节实测 1.4–10.3s，明显慢于普通推理接口。
客户端与反向代理的读超时建议 ≥120s。

---

## 10. 能力矩阵

**支持**

- `/v1/chat/completions` 流式与非流式
- `/v1/messages`（Claude，含 `system`、流式 SSE）
- `/v1/responses`（Codex，含流式 SSE）
- `/v1beta/models/{model}:generateContent`（Gemini）
- system 消息、多轮历史、tool 消息回放（均通过 transcript 传递）

**不支持**

- 客户端工具执行（function calling 的真实调用）
- 图片 / 音频 / 文件输入
- embedding、rerank、图像生成、语音（TTS/STT）
- 采样参数（`temperature`、`top_p`、`max_tokens`、`stop`、`seed` 等）
- `n > 1`、`logprobs`、`response_format` / JSON mode
- 服务端会话记忆（每次请求都要自带完整上下文）
- 精确 token 计量（`usage` 为估算值）
