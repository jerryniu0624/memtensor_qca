# QCA 渠道部署手册

本文讲**怎么把带 QCA 渠道的 new-api 部署起来**，以及上线前后**必须做的措施**。

| 文档 | 面向 | 内容 |
| --- | --- | --- |
| `QCA_DEPLOYMENT.md`（本文） | 运维 / 部署 | 构建、运行、环境变量、安全加固、渠道创建、反代与超时、验收、升级、检查清单 |
| `QCA_CHANNEL.md` | 管理员 / 开发 | 协议映射原理、改了 new-api 哪些文件、实测结果 |
| `QCA_API_USAGE.md` | 调用方 | 四种协议的请求示例、字段支持矩阵、错误对照 |

---

## 0. 结论先行

- **`git pull` 不等于能跑**：仓库里只有源码，必须构建。前端产物 `web/dist` 被
  `web/.gitignore:11` 忽略、没有入库，而 `main.go:44` 有 `//go:embed web/dist`，
  所以在干净克隆里直接 `go build .` 会失败。
- **推荐路径**：用仓库自带的 `Dockerfile` 构建（多阶段，第一阶段用 bun 建前端，
  第二阶段编译 Go），一条 `docker build` 搞定。
- 服务起来之后还有 5 件必须做的事：改默认管理员口令、固定 `SESSION_SECRET` /
  `CRYPTO_SECRET`、建 QCA 渠道、配模型价格或开自用模式、签发客户端令牌。
- 本次改动**没有数据库表结构变更**（只在渠道 `settings` 的 JSON 里多了一个
  `qca_environment_id` 字段），升级已有实例不需要迁移脚本。

---

## 1. 前置条件

| 项 | 要求 |
| --- | --- |
| 运行时 | Docker 20+（推荐），或 Go 1.25.1+ 加 bun 1.4（源码构建） |
| 数据库 | SQLite（默认，落在数据目录）/ MySQL ≥ 5.7.8 / PostgreSQL ≥ 9.6 |
| 缓存 | 可选 Redis（多节点或需要分布式限流时） |
| 出站网络 | 必须能访问 `api.qoder.com:443`；QCA 走 SSE 长响应，链路上不能强制缓冲或短超时 |
| QCA 资源 | Access Token（`pt-...`）、Environment ID（`env_...`）、每个对外模型一个 bridge-ready Agent（`agent_...`） |
| 容量 | QCA 渠道无状态（每请求新建 Session），实例可水平扩展，不需要会话粘性 |

---

## 2. 两个必知的坑

### 2.1 `web/dist` 不在仓库里，直接 `go build` 会失败

`main.go:44` 与 `main.go:47` 用 `//go:embed` 嵌入 `web/dist` 和 `web/dist/index.html`，
但 `web/dist` 被 `web/.gitignore:11` 忽略。解决方式二选一：

1. 先构建前端，再编译后端（见 §4）；
2. 用 `Dockerfile` 构建，第一阶段会把 `web/dist` 拷进编译上下文（见 §3）；
3. 只要 API、不要控制台时用 `Dockerfile.dev`，它在 `Dockerfile.dev:22` 放了一个占位
   `index.html`（见 §5）。

> 附带影响：本仓库改过前端（渠道类型下拉里的 QCA、Environment ID 输入框），
> **只有重新构建 `web/dist` 才会在控制台生效**。用旧 dist 编出来的二进制，
> 后端 QCA 渠道完全可用，但控制台里看不到 QCA 类型，只能走管理 API 建渠道（§8.3）。

### 2.2 仓库自带的 `docker-compose.yml` 拉的是官方镜像

`docker-compose.yml:19` 写的是 `image: calciumion/new-api:latest`，
直接 `docker compose up -d` 起来的是**不含 QCA 渠道的上游版本**。
必须改成自己构建的镜像：

```yaml
services:
  new-api:
    build: .                      # 或者 image: memtensor-qca:latest
    # image: calciumion/new-api:latest   # 注释掉这行
```

---

## 3. 部署方式 A：Docker（推荐）

### 3.1 构建镜像

```bash
git clone git@github.com:jerryniu0624/memtensor_qca.git
cd memtensor_qca
docker build -t memtensor-qca:latest .
```

构建过程需要联网拉基础镜像与依赖：`oven/bun:1.4.0`、`golang:1.26.1-alpine`、
`debian:bookworm-slim`，以及 npm / Go module 依赖。内网环境请先准备好镜像与代理。

### 3.2 运行（SQLite，单机最简）

```bash
docker run -d --name new-api-qca \
  --restart unless-stopped \
  -p 3000:3000 \
  -v "$(pwd)/data:/data" \
  -e PORT=3000 \
  -e TZ=Asia/Shanghai \
  -e SESSION_SECRET="$(openssl rand -hex 32)" \
  -e CRYPTO_SECRET="$(openssl rand -hex 32)" \
  memtensor-qca:latest
```

### 3.3 运行（外置数据库 + Redis，生产建议）

```bash
docker run -d --name new-api-qca \
  --restart unless-stopped \
  -p 3000:3000 \
  -v "$(pwd)/data:/data" \
  -e PORT=3000 \
  -e TZ=Asia/Shanghai \
  -e SESSION_SECRET='固定随机串' \
  -e CRYPTO_SECRET='固定随机串' \
  -e SQL_DSN='postgresql://user:pass@db-host:5432/new_api' \
  -e REDIS_CONN_STRING='redis://:pass@redis-host:6379' \
  -e MEMORY_CACHE_ENABLED=true \
  -e RELAY_TIMEOUT=0 \
  memtensor-qca:latest
```

### 3.4 用仓库 compose

按 §2.2 改完 `build: .` 之后：

```bash
docker compose up -d --build
docker compose logs -f new-api
```

compose 里默认带 PostgreSQL 与 Redis，并且 `SQL_DSN` / `REDIS_CONN_STRING` 里是示例口令
（`docker-compose.yml:29`、`docker-compose.yml:34`），**上线前必须改掉**。

---

## 4. 部署方式 B：本机源码构建

```bash
git clone git@github.com:jerryniu0624/memtensor_qca.git
cd memtensor_qca

# 1) 前端（必须，否则 go build 会因为缺 web/dist 失败）
cd web
bun install --frozen-lockfile
DISABLE_ESLINT_PLUGIN=true VITE_REACT_APP_VERSION=$(cat ../VERSION) bun run build
cd ..

# 2) 后端
go build -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version=$(cat VERSION)'" -o new-api .

# 3) 运行
PORT=3000 ./new-api
```

仓库里的 `make all`（`Makefile:13`）等价于「构建前端 + `go run main.go`」，适合开发调试；
生产建议按上面编译出二进制再交给 systemd / 容器管理。

自检命令（提交前后都值得跑一遍）：

```bash
gofmt -l relay/channel/qca                 # 无输出
go build ./...                             # 通过
go test ./relay/channel/qca/...            # ok
cd relaykit && GOWORK=off go build ./...   # relaykit 模块独立性，通过
```

---

## 5. 部署方式 C：只要 API、不要控制台

```bash
docker build -f Dockerfile.dev -t memtensor-qca-api:latest .
```

`Dockerfile.dev` 跳过前端构建，用占位 `index.html` 满足 embed。代价是控制台不可用，
渠道、令牌、计价全部只能走管理 API（§8.3）。适合纯网关形态的部署。

---

## 6. 运行时配置

### 6.1 常用环境变量

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `PORT` | 3000 | 监听端口 |
| `SQL_DSN` | 空（用 SQLite） | 主库连接串，MySQL / PostgreSQL |
| `LOG_SQL_DSN` | 空（同主库） | 日志库，可单独指 ClickHouse |
| `SQLITE_PATH` | 数据目录 | SQLite 文件路径 |
| `REDIS_CONN_STRING` | 空 | 配了才启用 Redis（分布式限流、多节点同步） |
| `SESSION_SECRET` | 随机 | **多节点或重启不想掉登录态就必须固定** |
| `CRYPTO_SECRET` | 随机 | **固定它**，否则渠道密钥等加密数据的解密会受影响 |
| `MEMORY_CACHE_ENABLED` | false | 内存缓存；开启后配置变更靠同步生效（见 §6.3） |
| `SYNC_FREQUENCY` | 60 | 节点间同步秒数 |
| `NODE_TYPE` | master | 从节点设 `slave` |
| `RELAY_TIMEOUT` | 0（不限） | 转发 HTTP 客户端整体超时（秒），见 §6.2 |
| `BATCH_UPDATE_ENABLED` | false | 批量落库用量，高并发时降低 DB 压力 |
| `CHANNEL_UPDATE_FREQUENCY` | 未设 | 渠道缓存刷新秒数 |
| `DEBUG` | false | 调试日志 |
| `GIN_MODE` | release | 设 `debug` 会打印 gin 调试信息 |

### 6.2 QCA 渠道必须关注的两项

- **`RELAY_TIMEOUT`**：这是转发用的 `http.Client.Timeout`（`service/http_client.go:129`），
  覆盖整轮请求。QCA 每轮要新建 Session，实测首字节 1.4–10.3s，长回答更久。
  建议保持 `0`（不限，由客户端与反代控制），若一定要设，**不要低于 300 秒**。
- **渠道级 `proxy`**：如果 new-api 出网需要代理才能到 `api.qoder.com`，
  在渠道的其它设置里填 `proxy`（对应 `relaykit/dto/channel_settings.go:17`，
  控制台是「代理」输入框），只对该渠道生效，不影响其它渠道。

### 6.3 多节点与缓存

- 所有节点的 `SESSION_SECRET`、`CRYPTO_SECRET` 必须一致，且指向同一个数据库。
- 开了 `MEMORY_CACHE_ENABLED` 后，改渠道 / 改选项不会立刻在所有节点生效，
  最长要等 `SYNC_FREQUENCY` 秒；排查配置类问题时可以先关内存缓存复现。
- QCA 渠道无状态，**不需要**会话粘性或渠道亲和，多实例直接负载均衡即可。

---

## 7. 初始化与安全加固（必要措施）

1. **改掉默认管理员口令**：库里没有任何用户时，new-api 会自动创建
   `root` / `123456`（`model/main.go:70` `createRootAccountIfNeed`，日志里也会打印）。
   首次登录后立刻改密码，并开启 MFA / WebAuthn。
2. **关闭公开注册**：系统设置里把 `RegisterEnabled`、`PasswordRegisterEnabled` 关掉，
   账号由管理员创建。
3. **固定 `SESSION_SECRET` / `CRYPTO_SECRET`**：不固定会导致重启后登录态失效，
   以及加密存储的渠道密钥解密异常。
4. **后台不要裸露公网**：控制台走内网 / VPN，或反代上加 TLS + IP 白名单 + 限速。
5. **QCA PAT 的保管与轮换**：PAT 只存在渠道「密钥」里；不要写进仓库、前端代码、
   日志或工单。轮换流程：在 QCA 侧新建 PAT → 更新渠道密钥 → 吊销旧 PAT。
   更新渠道密钥不需要重启服务。
   渠道密钥在 `GET /api/channel/` 与 `GET /api/channel/{id}` 里都不回显（返回空字符串），
   要读完整值只能用 `POST /api/channel/{id}/key`，而该接口要求 root 权限并触发二次安全校验
   （`router/channel-router.go:23`）；令牌 key 同理（§8.3）。
6. **令牌最小权限**：给客户端的 `sk-...` 令牌要设分组、有效期（`expired_time`）、
   额度上限（`remain_quota`，默认 500000 额度 = 1 美元，见 `common/constants.go:22`），
   必要时配 IP 白名单；不要用 `unlimited_quota: true` 发给外部调用方。
7. **审计**：new-api 对令牌、渠道等敏感操作有审计记录，保留日志库并接入告警。
   任何日志、截图、issue 里都不要出现完整 PAT 或 `sk-` 令牌。

---

## 8. 建 QCA 渠道

### 8.1 QCA 侧：准备 Agent

每个对外模型对应一个 Agent。`POST /agents` 只需 `name` / `model` / `system` 三个已验证字段：

```bash
export QCA_BASE_URL="https://api.qoder.com/api/v1/cloud"
export QCA_TOKEN="pt-xxxxxxxx"

curl -sS -X POST "$QCA_BASE_URL/agents" \
  -H "Authorization: Bearer $QCA_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"deepseek-coding-agent","model":"dfmodel","system":"<bridge system prompt>"}'
```

- `model` 用 QCA 的抽象 id：`dfmodel`(DeepSeek-Flash)、`dmodel`(DeepSeek-V4-Pro)、
  `kmodel_latest`(Kimi-K3)、`gmodel`(GLM-5.3)、`qmodel`(Qwen3.7-Plus)。
- `system` 必须是 **bridge-ready**：把 `<|system|>` 块当作权威系统指令并完全遵循、
  把 `<|user|>` / `<|assistant|>` / `<|tool|>` 当作已有对话、只回答最后一个 `<|user|>` 块、
  只输出正文、不复述指令、不透露自己是 bridge、不用 runtime 名字自我介绍。
  否则客户端传的 system / instructions 会被当成普通数据忽略。
- 返回体里的 `id` 就是 Agent ID，记下来填进模型映射。
- 已有 Agent 只想改 system：`PUT /agents/{id}`，body 只发 `{"system":"..."}`；
  该接口是 merge，`model` / `name` / `tools` 不受影响。
- Agent 的 `name` 对模型可见，取中性名字（例如 `deepseek-coding-agent`）。
- 工具要配在 Agent 侧（`agent_toolset` / `mcp_servers`）；依赖客户端执行工具的 Agent
  会让请求以 `stop_reason=requires_action` 失败。

### 8.2 控制台创建（前提：前端已按本仓库重新构建）

1. 渠道 → 新建渠道，在「选择供应商」弹窗里选 **QCA**（属「内置」分类；搜索框可直接输
   `QCA` 或类型号 `64`）。
2. 密钥：QCA 的 PAT（`pt-...`）。
3. Base URL：留空即用内置默认 `https://api.qoder.com/api/v1/cloud`；要走出网代理才填，
   或在「代理」字段单独配代理。
4. QCA Environment ID：`env_...`（**必填**，缺失时请求直接报错）。
5. 模型：声明要对外暴露的模型名，例如 `glm-5.3-qca`（渠道不内置模型列表）。
6. 模型映射：把每个模型名映射到它的 Agent ID —— 这是选定 Agent 的唯一方式。
7. 测试模型：填一个已映射的模型名，便于用「测试」按钮巡检（会真实调用 QCA）。

> **类型列表里找不到 QCA？** 说明当前控制台的前端产物不是本仓库构建的。上游 new-api 没有
> 64 这个类型，而 `web/dist` 被 `.gitignore` 排除、只在构建镜像时由 `bun install && bun run build`
> 生成（见 §2.1），所以用官方镜像或旧前端产物部署时必然看不到 QCA。
> 自检：`grep -rl "Qoder Cloud Agent" web/dist` 有命中即为已构建；没有命中就按 §3.1 重新构建镜像，
> 或按 §4 重新构建前端后重启进程。前端一时构建不出来时，直接用 §8.3 的管理 API 建渠道，
> 渠道建成后列表里会以 QCA 徽标显示，转发功能不受前端影响。

落库后的关键字段等价于：

```json
{
  "type": 64,
  "base_url": "https://api.qoder.com/api/v1/cloud",
  "key": "pt-xxxxxxxx",
  "models": "glm-5.3-qca,kimi-k3-qca",
  "model_mapping": "{\"glm-5.3-qca\":\"agent_xxx\",\"kimi-k3-qca\":\"agent_yyy\"}",
  "settings": "{\"qca_environment_id\":\"env_xxx\"}"
}
```

### 8.3 管理 API 全流程（无控制台时用它，命令均已实测）

```bash
BASE=http://127.0.0.1:3000

# 1) 登录拿 access_token（管理接口用它做 Bearer）
TOKEN=$(curl -s "$BASE/api/user/login" -H 'Content-Type: application/json' \
  -d '{"username":"root","password":"你的密码"}' \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["access_token"])')

# 2) 开自用模式（若打算给每个模型配价格，可跳过这步）
curl -s -X PUT "$BASE/api/option/" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"key":"SelfUseModeEnabled","value":"true"}'

# 3) 建 QCA 渠道（type 64）
curl -s -X POST "$BASE/api/channel/" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{
  "mode": "single",
  "channel": {
    "type": 64,
    "name": "qca-native",
    "key": "pt-xxxxxxxx",
    "base_url": "",
    "models": "glm-5.3-qca,kimi-k3-qca",
    "group": "default",
    "model_mapping": "{\"glm-5.3-qca\":\"agent_xxx\",\"kimi-k3-qca\":\"agent_yyy\"}",
    "test_model": "glm-5.3-qca",
    "setting": "{}",
    "settings": "{\"qca_environment_id\":\"env_xxx\"}"
  }}'

# 4) 签发客户端令牌（生产建议给额度与有效期，不要 unlimited）
curl -s -X POST "$BASE/api/token/" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"client-a","remain_quota":5000000,"unlimited_quota":false,"expired_time":-1,"group":"default"}'

# 5) 取令牌完整 key（列表接口返回的是打码 key，必须用这个接口）
curl -s -X POST "$BASE/api/token/1/key" -H "Authorization: Bearer $TOKEN"
# -> {"success":true,"data":{"key":"sk-..."}}
```

> `GET /api/token/` 与 `GET /api/token/{id}` 返回的 key 是打码的
> （形如 `pzxx**********tRuu`，见 `model/token.go:63` `MaskTokenKey`），
> 完整 key 只能从 `POST /api/token/{id}/key` 拿，或在创建时立即保存。

上面这套管理 API 命令都在真实实例上验证过，顺带确认了三件事：

- **内置默认地址已注册**：`GET /api/channel/default_base_urls` 返回
  `"64": "https://api.qoder.com/api/v1/cloud"`，所以渠道 `base_url` 可以留空。
- **留空 `base_url` 确实回落到默认地址**：用一个 `base_url` 为空、PAT 为假值的
  type 64 渠道发请求，返回的是 QCA 自己的错误
  `HTTP 401 {"error":{"message":"invalid pt-token",...}}`，
  说明请求打到了 `api.qoder.com`，且上游非 2xx 的状态码与错误体被原样透传。
- **建渠道 / 建令牌的 payload 可用**：§8.3 的两个 POST 都返回 `{"success":true}`；
  `POST /api/token/{id}/key` 能拿到完整 key（48 字符），列表接口只能拿到打码 key。

### 8.4 计价（必须处理，否则请求会被拦）

QCA 渠道的模型名默认没有价格。二选一：

- 开**自用模式**（选项 `SelfUseModeEnabled`）：不校验价格，适合内部自用；
- 或在「模型价格」里给每个模型名配单价。

注意 QCA 的 `usage` 是**估算值**（QCA 不返回 token 用量，由 `estimateUsage` 估算），
同一句提示词估算可能只有上游真实计数的几十分之一。因此：
**不要按上游真实 token 口径给 QCA 模型定价**，否则会严重少收；
要么按估算口径定价，要么走自用模式只做统计。

---

## 9. 反向代理与超时（必要措施）

QCA 走 SSE，且首字节明显慢于普通推理接口（实测 1.4–10.3s）。反代必须关闭缓冲、
放宽读超时，否则流式会退化成一次性返回，长回答会被中途掐断。

```nginx
location / {
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Connection        "";

    # 流式 / SSE：不缓冲、不缓存、不压缩
    proxy_buffering off;
    proxy_cache off;
    gzip off;
    chunked_transfer_encoding on;

    # QCA 一轮 turn 可能很久
    proxy_connect_timeout 30s;
    proxy_send_timeout    300s;
    proxy_read_timeout    300s;
}
```

客户端侧同样要把读超时放宽到 ≥120s（SDK 默认值通常偏小）。

---

## 10. 部署后验收清单

```bash
KEY="sk-你的令牌"; API="https://your-host"

# 1) 服务活着
curl -s "$API/api/status" -o /dev/null -w "status=%{http_code}\n"

# 2) 模型可见
curl -s "$API/v1/models" -H "Authorization: Bearer $KEY"

# 3) OpenAI 非流式
curl -s "$API/v1/chat/completions" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","messages":[{"role":"user","content":"只回复两个字：在的"}]}'

# 4) OpenAI 流式，期望帧序 role -> content... -> finish_reason=stop -> usage -> [DONE]
curl -sN "$API/v1/chat/completions" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","stream":true,"messages":[{"role":"user","content":"只回复两个字：在的"}]}'

# 5) 多轮上下文（网关无状态，必须自带完整 messages）
curl -s "$API/v1/chat/completions" -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"kimi-k3-qca","messages":[{"role":"system","content":"你只用中文回答，且不超过10个字。"},{"role":"user","content":"我叫小明。"},{"role":"assistant","content":"你好小明"},{"role":"user","content":"我叫什么？"}]}'

# 6) Claude / Responses / Gemini 三个入口
curl -s "$API/v1/messages" -H "x-api-key: $KEY" -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"kimi-k3-qca","max_tokens":512,"messages":[{"role":"user","content":"只回复两个字：在的"}]}'
curl -s "$API/v1/responses" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.3-qca","input":"只回复两个字：在的"}'
curl -s -X POST "$API/v1beta/models/glm-5.3-qca:generateContent" -H "x-goog-api-key: $KEY" \
  -H "Content-Type: application/json" \
  -d '{"contents":[{"role":"user","parts":[{"text":"只回复两个字：在的"}]}]}'
```

验收通过标准（均为实测值）：4 个入口都返回 200；回复文本正确；
`finish_reason=stop` / `stop_reason=end_turn` / `finishReason=STOP`；
日志与用量记录里能看到这次调用；额度按预期扣减。

---

## 11. 运维措施

- **监控项**：QCA 渠道的首字节延迟与整体耗时（正常量级 1.4–10.3s）、错误率、
  401（PAT 失效）、5xx（上游异常）、`requires_action`（Agent 工具配置不当）。
- **自动禁用**：`AutomaticDisableChannelEnabled` 默认是 `false`（`common/constants.go:129`）。
  开启后遇到认证类失败会自动禁用渠道，**务必同时配告警**，否则会静默停用；
  `AutomaticEnableChannelEnabled` 控制自动恢复。
- **重试**：`RetryTimes` 默认 `0`（`common/constants.go:134`），即不自动换渠道重试。
  QCA 每轮新建 Session，重试等于重跑一整轮，会再消耗一次 QCA 额度，按需开启并设小值。
- **渠道测试**：控制台的「测试」按钮会真实调用 QCA（用 `test_model`），巡检会消耗额度。
- **灰度**：先只放一个模型、一个小额度令牌给内部试用，观察延迟与错误率，再逐步放开。
- **回滚**：QCA 渠道可以单独停用（不影响其它渠道）；客户端侧把令牌分组切回原有渠道即可。
  代码层回滚就是把镜像换回上游版本，数据库无需回退（本次无表结构变更）。
- **备份**：定期备份数据库（渠道配置、令牌、用量都在里面）；QCA 的 PAT 与 Agent ID
  另做一份离线记录，PAT 不要进备份明文以外无处可查。
- **额度与用量口径**：向使用方说明 QCA 的 token 数是估算值，避免和上游真实计数对账。

---

## 12. 升级流程

```bash
cd memtensor_qca
git pull                                     # 或 git fetch && git checkout <tag/commit>
docker build -t memtensor-qca:$(git rev-parse --short HEAD) .
docker tag memtensor-qca:$(git rev-parse --short HEAD) memtensor-qca:latest

# 停旧起新，挂同一个数据卷
docker stop new-api-qca && docker rm new-api-qca
docker run -d --name new-api-qca ... memtensor-qca:latest   # 参数同 §3.2 / §3.3

# 升级后按 §10 复验
```

启动时 GORM `AutoMigrate` 会自动对齐表结构，本次改动没有新增表或列，
升级前后数据库结构一致；仍然建议升级前先备份数据库。

---

## 13. 上线检查清单

| 检查项 | 状态 |
| --- | --- |
| 前端已重新构建（`web/dist` 是本次代码产物），或已确认走纯 API 形态 | ☐ |
| 镜像/二进制由本仓库构建，不是 `calciumion/new-api:latest` | ☐ |
| `SESSION_SECRET`、`CRYPTO_SECRET` 已固定并安全保存 | ☐ |
| 默认 `root`/`123456` 已改，MFA 已开，公开注册已关 | ☐ |
| 数据库已外置（生产）且已配置备份 | ☐ |
| 出网可达 `api.qoder.com:443`，或渠道 `proxy` 已配 | ☐ |
| QCA PAT、Environment ID、各模型 Agent ID 已登记并离线备份 | ☐ |
| 每个 Agent 的 system 是 bridge-ready（客户端 system 能生效） | ☐ |
| 渠道模型映射覆盖所有对外模型名 | ☐ |
| 模型价格已配，或已开自用模式 | ☐ |
| `RELAY_TIMEOUT` 为 0 或 ≥300s，反代 `proxy_buffering off` 且读超时 ≥300s | ☐ |
| 客户端令牌有分组、额度、有效期（必要时 IP 白名单） | ☐ |
| §10 的 6 项验收全部通过 | ☐ |
| 监控与告警已接（延迟、错误率、401、自动禁用事件） | ☐ |

---

## 14. 排障入口

- 调用报错（含网关原样返回的错误文本对照表）→ `QCA_API_USAGE.md` §9
- 协议映射原理与代码改动清单 → `QCA_CHANNEL.md` §1、§3
- 上游 5xx：先绕过 new-api 直连上游确认。实测 `kimi-k3` 在基线渠道返回 503
  （`model_not_found`），直连上游同样 503，属于上游故障而非本渠道问题。
- 配置改了不生效：确认是否开了 `MEMORY_CACHE_ENABLED`（最长需等 `SYNC_FREQUENCY` 秒），
  以及是否改的是当前节点连的那套数据库。
