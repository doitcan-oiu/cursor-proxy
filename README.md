# Cursor → OpenAI 反代（Go 版）

把 Cursor 订阅暴露成 OpenAI / Anthropic 兼容 API 的反向代理，自管 API Key。
本目录是原 Node + Electron 项目的 Go 重写版：**单一静态二进制**，内置浏览器管理界面。

## 快速开始

```bash
cd go-script
cp .env.example .env        # 按需修改；ADMIN_TOKEN 留空会自动生成

make build                  # 构建管理界面 + 二进制
# 没装 make 就用：./build.sh
./cursor-proxy
```

启动后终端会打印管理界面地址与 `ADMIN_TOKEN`，浏览器打开即可操作：

```
============================================================
  Cursor -> OpenAI 反代已启动 (Go)
  Base URL   : http://0.0.0.0:3100/v1
  管理界面   : http://127.0.0.1:3100  ← 浏览器打开，用下面的口令登录
  Admin token: admin_xxxxxxxxxxxx
============================================================
```

> 只改后端、不动界面时可以用 `make build-go`（沿用上次的界面产物）或 `go run ./cmd/server`。

## 管理界面

Vue 3 + Tailwind CSS v4 + shadcn-vue，视觉遵循 [`DESIGN.md`](./DESIGN.md) 的设计系统
（纯白画布、近黑药丸按钮、DM Sans、品牌色产品卡）。构建产物由 `go:embed` 打进二进制，
运行时不需要 Node，也不依赖外网（字体已自托管）。

| 页面 | 能做什么 |
| --- | --- |
| 概览 | 可用账号、近一分钟请求、成功率、模型数；接入信息一键复制；账号健康与近期请求 |
| 账号池 | 批量导入 / 单条添加（web token 自动换取）、验号（套餐与用量）、独立出口代理、删除 |
| 访问密钥 | 签发 / 吊销 sk- 密钥，明文仅创建时展示一次 |
| 模型 | 拉取上游可用模型，搜索、复制模型名 |
| 调试台 | 选账号与模型跑一次真实对话，SSE 实时输出，含思考过程 |
| 出口网络 | 内置 Mihomo：订阅、策略（自动测速/故障转移/负载均衡）、节点测速与切换 |
| 请求日志 | 实时轮询、统计条、只看失败、错误详情、失败请求原文可查可导出 |

界面用 `ADMIN_TOKEN` 登录，口令只存在浏览器 localStorage。

### 前端开发

```bash
make ui-deps      # 首次安装依赖
make dev-ui       # Vite 热更新，API 自动代理到 127.0.0.1:3100
make run          # 另开一个终端跑 Go 服务
```

### 三步跑通

```bash
# 1) 存一条 Cursor 凭证（浏览器 Cookie 里的 WorkosCursorSessionToken，或桌面 accessToken）
curl -X POST http://127.0.0.1:3100/admin/cursor-tokens \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"token":"user_xxx::eyJ...","label":"my-account"}'

# 2) 生成一把对外代理 Key
curl -X POST http://127.0.0.1:3100/admin/keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" -d '{"name":"my-key"}'

# 3) 用返回的 sk-... 调用（OpenAI 兼容）
curl http://127.0.0.1:3100/v1/chat/completions \
  -H "Authorization: Bearer sk-xxxx" \
  -d '{"model":"auto","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

## 对外接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/models` | 列出可用模型（需代理 Key） |
| POST | `/v1/chat/completions` | OpenAI 兼容对话，支持 `stream` |
| POST | `/v1/messages` | Anthropic 兼容对话，支持 `stream` |
| GET | `/cursor/loginDeepControl` | 用 web token 走 PKCE 换取可用 token |

鉴权：OpenAI 用 `Authorization: Bearer sk-...`；Anthropic 用 `x-api-key: sk-...`。

### 关于工具调用（function calling）

支持 OpenAI 的 `tools` / `tool_calls` 与 Anthropic 的 `tools` / `tool_use`，
流式与非流式都可用，因此能对接 OpenCode 这类需要工具调用的 agent 客户端。

实现方式是**提示词模拟**而非协议透传，原因是上游不具备这个能力：Cursor 的 agent
端点用 protobuf 字段号标识自己内置的工具（如字段 7 = 读文件），协议里没有任何位置
能承载客户端声明的具名函数与 JSON Schema。

所以这里把客户端声明的工具写进提示词，约定模型用 `<tool_call>` 标签输出调用意图，
再从输出流里剥离该标签、还原成标准的 `tool_calls` / `tool_use`。要注意：

- **只为上游没有内置对应物的自定义工具注入**。`read` / `bash` / `grep` 这类
  上游本来就有，走原生桥接更可靠；两套协议同时存在反而会出问题（见下）。
- 可靠性取决于模型是否遵守输出格式，并行多工具调用比原生能力弱。
- 标签解析失败时会把原文原样返回，不会静默吞掉内容。
- 请求里没有 `tools` 字段时完全不注入，普通对话的行为不受任何影响。

**提示词里不能说假话**。早期这段提示写的是「你没有任何内置工具，只能靠
`<tool_call>` 块行动」——那是原生桥接还不存在时用来逼模型走文本协议的说法。
上游本身就是带内置工具的 agent，这句话它一眼就能看穿，于是把整段注入连同
对话历史一起判定为「伪造的代理对话记录」，明确拒绝采用，然后反复重试同一个
内置工具。现在的措辞只声明「这是客户端提供的额外工具」，不否认它已有的能力。

主要路径是**桥接上游的原生工具调用**。Cursor 的 agent 并不自己执行工具，而是把调用
下发给客户端执行；这些调用原先被整个忽略，表现就是「模型说要做某事，然后对话断了」。
现在会解析出来并按名称与 JSON Schema 映射到客户端声明的对应工具。已覆盖的内置工具：

字段号与参数结构**抄自 Cursor 客户端自带的 protobuf 描述**
（`/usr/share/cursor/resources/app/extensions/cursor-local-agent-runtime/dist/main.js`
里的 `agent.v1.ToolCall`），不是靠抓帧猜的——早期靠猜错过两次
（字段 4 当成 ls 其实是 glob，字段 23 当成待办其实是向用户提问）。

| 上游工具（`agent.v1.ToolCall` 字段号） | 映射到客户端的 |
| --- | --- |
| `shell` (1) | `bash` / `shell` / `run_terminal_cmd` … |
| `delete` (3) | `delete` / `rm` / `remove` … |
| `glob` (4) | `glob` / `file_search` / `find_files` … |
| `grep` (5) | `grep` / `search` / `ripgrep` … |
| `read` (8) | `read` / `view_file` / `cat` … |
| `update_todos` (9) | `TodoWrite` / `todos` / `update_plan` …（按客户端 schema 合成数组） |
| `edit` (12) | `write` / `edit` / `create_file` … |
| `ls` (13) | `ls` / `list_dir` / `list_directory` … |
| `web_search` (18) | `web_search` / `search_web` … |
| `task` (19) | `task` / `agent` / `subagent` … |
| `ask_question` (23) | 文本说明（本就是给用户看的） |
| `fetch` (24) / `web_fetch` (37) | `webfetch` / `fetch` / `read_url` … |
| `await` (42) | 文本说明 |

其余 40 多个内置工具（`generate_image`、`computer_use`、`pi_*` 系列等）
只登记了规范名，暂不解析参数；真被用到时客户端会收到一句带工具名的说明。

文本协议作为补充，覆盖上游没有内置对应物的自定义工具。
用 `NATIVE_TOOL_BRIDGE=off` 可关闭桥接。

**遇到没映射的工具怎么办**：未识别的工具不会被静默丢弃。管理界面有一个
**「未识别工具」页面**（侧栏带红色角标提示数量），记录工具的规范名、字段号、
触发它的模型、参数结构与原始字节，可一键复制或导出 JSON。同时服务端日志也会打印一行提示，
客户端则收到一句说明文本而不是空回复。

补映射时把字段号与参数结构填进 `internal/cursor/agent.go` 的 `toolParsers`
和 `internal/tools/bridge.go` 的 `candidateNames` 即可，各加一行。

**待办清单按客户端声明的 schema 生成**，不写死字段。各家要求并不一致：
OpenCode 的 `todowrite` 要求 `content` / `status` / `priority` 三个全填、且没有 `id`，
缺一个就报 `SchemaError(Missing key at ["todos"][0]["priority"])` 让整个调用作废；
Claude Code 的 `TodoWrite` 要的则是 `activeForm`。代理会读取工具的 items schema，
只填它声明过的字段，认不出来但必填的字段按类型补占位值。

**工具宣告了却没做完**：上游偶尔只发一个「进行中」帧宣告要用某工具，
然后既不给参数也不发完成帧（实测 `web_search` 就是这样，参数长度 0）。
若这一轮同时什么都没产出，客户端只会收到模型那句「我这就去搜索…」然后流正常结束，
看起来像说完了——代理会在收尾时补一句说明指出是哪个工具没做完。

只在「这一轮什么都没产出」时才提示。模型宣告了工具又放弃很常见（想了想改主意、
或换个 id 重发），只要有正文或完成了别的调用，这种半途而废就是无害的中间状态，
不会打扰到回复内容。

实测（OpenCode 完成「创建文件 → 运行 → 汇报输出」）：

| 模型 | 结果 |
| --- | --- |
| `claude-4.6-opus-max` | 可用 |
| `claude-4.5-sonnet` | 可用 |
| `gpt-5.1` | 可用 |
| `composer-2.5` | 可用 |
| `auto` / `default` | 不可用——它是 Cursor 的 agent 路由器，只回一句「我这就去创建…」就结束 |

**接 agent 客户端时不要用 `auto`**，指定具体模型。

### 对接 OpenCode

OpenCode 用 `@ai-sdk/anthropic` 走 `/v1/messages`。有个坑：**自定义模型默认不带工具能力**，
必须显式写 `"tool_call": true`，否则 OpenCode 根本不会发送 `tools` 字段，
模型没有工具可用，就会只回一句话然后结束。

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "cursorproxy": {
      "npm": "@ai-sdk/anthropic",
      "name": "cursorproxy",
      "options": {
        "apiKey": "sk-你的代理密钥",
        "baseURL": "http://127.0.0.1:3100/v1"
      },
      "models": {
        "claude-4.5-sonnet": {
          "name": "claude-4.5-sonnet",
          "tool_call": true,
          "reasoning": false,
          "limit": { "context": 200000, "output": 32000 }
        }
      }
    }
  }
}
```

排查对接问题时用 `PROXY_DEBUG_BODY=1` 启动，可以看到客户端发来的完整请求体
与我们返回的响应体（包括 SSE 事件流）。

**失败请求会自动留档**，不必开调试开关也能事后复现：请求日志页面上，
每条失败记录的错误信息下方有「查看请求 / 复制 / 导出 JSON」三个按钮，
点开就是那次请求的原文。几点说明：

- 只有失败的请求会留，成功的不留，避免正常流量白占内存。
- 内嵌图片的 base64 载荷会替换成 `<image/png base64 已省略，约 N KB>`，
  一张图能有几百 KB，留着既撑爆内存也对排查没用。
- 单条上限 16KB，超出会截断并标注原始长度；最多为最近 30 条失败保留原文。
- 导出的 JSON 里还带上时间、模型、HTTP 状态码、耗时和错误信息，
  发给别人就能直接复现。

### 纯对话（不声明 tools）时的行为

上游是 agent 形态：问它「写一段 SVG」会把内容塞进一次「写文件」调用而不是直接回答，
在聊天客户端里表现为只回一句「我这就写…」然后没有下文。代理会把这类调用还原成正文
（见下），所以纯聊天客户端用起来没有区别。

上游确实还有一个 ask 模式（`UserMessage.mode`，`agent.v1.AgentMode` 的 `ASK = 2`），
能让模型直接作答、省掉这层还原。**但默认不开**，因为它不是通用问答模式，而是 Cursor 里
「就代码库提问」的模式：上游会告诉模型「你只能回答代码和代码库相关的问题」。实测的后果是
模型会拿它当拒绝理由——

> I'm in Ask mode, so I can't generate creative writing, roleplay content,
> or produce the interactive fiction output you're requesting.

确定只拿这个代理问代码的话，可以设 `ASK_MODE=on` 打开；声明了 `tools` 的请求
任何时候都留在 agent 模式，否则内置工具桥接就没得桥了。

**已经被拒绝过的会话救不回来**。模型拒绝之后，那句话就留在对话历史里，
后面每轮都会被回放给它，它会跟自己上一轮保持一致继续拒绝——换掉模式也只是
换个说法（agent 模式下变成「I'm a coding assistant designed to help with
software engineering tasks」）。改配置、重启代理都不解决这个。办法是开一条新对话，
或者在客户端里把那条拒绝从历史中删掉：实测历史正常的会话完全不受影响，
不必丢掉整个会话。

代理会把这种调用的内容还原成回复正文，所以纯聊天客户端能正常拿到内容，
而且是**逐字流式**的：上游本来就在分片下发这些内容（`sm.15` 帧），
代理边收边转发，不等整个调用结束。实测一个约 7 千字的 SVG，
首字 3.8 秒到达并持续输出，而不是静默 57 秒后整段弹出。

包装方式按文件类型决定，不针对某一种内容：

- 代码与标记语言（`.py` `.svg` `.html` `.json` `.conf` …）包成对应语言的代码块。
- 文章类（`.md` `.txt` `.rst`，以及没有扩展名的）原样输出，不套代码块——
  否则让它写文案、写 README 时会整篇变成灰底源码。
- 内容里自带 ` ``` ` 时外层围栏会自动加长，不会被提前截断。
- 上游的路径帧比首个内容片段晚到一帧，所以开头会多等一片再决定语言标记；
  路径始终没来就按内容开头猜（XML / HTML / JSON / 脚本），猜不出退化成无语言代码块。

声明了 `tools` 的客户端不走这条路径，调用会原样转成 `tool_calls` / `tool_use`。

### 多模态（图片）

两个端点都支持带图提问，图片会真正送到模型，不是拍平成文字后丢掉：

- OpenAI：`content` 数组里的 `{"type":"image_url","image_url":{"url":"..."}}`，
  `url` 支持 `data:` 内联与 `http(s):` 链接（后者由代理代为拉取）。
- Anthropic：`{"type":"image","source":{...}}`，支持 `type: base64` 与 `type: url`；
  `tool_result` 里夹带的图片也会被取出。
- 单张上限 20MB，超了直接拒绝；坏图只跳过并记一行日志，不会让整轮对话失败。
- 调试台可以直接点选、粘贴截图或拖入图片来试。

实现上，图片挂在 agent 协议的 `UserMessage.selected_context.selected_images` 里
（字段号取自 Cursor 客户端自带的 `agent.v1` 描述），走内联 `data` 而不是先上传拿 blob。

模型能力实测：`claude-4.6-opus-max`、`gpt-5.2`、`gemini-3.1-pro`、`cursor-grok-4.6-high`、
`composer-2.5`、`glm-5.2-high` 都能正确读图；`kimi-k3-high` 会直接回「无法查看图片」。
不支持视觉的模型只是答不出来，不会报错。

### 推理内容

`/v1/chat/completions` 以 `reasoning_content` 增量给出，
`/v1/messages` 以标准的 `thinking` 内容块给出（排在正文之前）。
思考型模型的推理往往比正文还长——实测一道推理题推理 3201 字、正文 562 字，
这部分 token 照样计费，不该丢掉。

### 慢在哪：用 `PROXY_TIMING=1` 看分解

`PROXY_TIMING=1` 启动后，每个请求会打印一行阶段耗时：

```
[timing] messages claude-opus-5-thinking-max 总 5m11s ::
  读请求体 <1ms | 解析消息 5ms | 算 token 41ms | 取号(含刷token) <1ms |
  上游握手 311ms | ★上游首个产出 4m56.8s | 流式输出 …
```

实测一次完整的 agent 会话（OpenCode 跑代码排查，11 个请求、32 次工具调用、共 14.9 分钟）：

| 阶段 | 占比 |
| --- | --- |
| 上游思考（首个产出之前） | **93.8%** |
| 上游握手（TLS + 一个 RTT） | 0.5% |
| 代理自身（解析 / 算 token / 取号） | 0.03% |

代理本身不是瓶颈。**决定速度的是模型的推理档位与上下文长度**——同一会话里思考时间
随上下文增长得很快：

| 输入 tok | 上游思考 | 输出 tok |
| --- | --- | --- |
| 583 | 1.3s | 10 |
| 12,129 | 14.1s | 52 |
| 20,550 | 93.4s | 40 |
| 47,499 | 296.8s | 89 |

注意输出只有几十个 token（就是一次工具调用），却要先花几分钟读完整个上下文。
agent 会话每轮都要重传全部历史，所以轮次越多越慢。试过两条路都不通：
上游忽略 `conversation_history`（见下文），固定 `conversation_id` 也不会让它保留会话状态
（实测第二轮就不记得第一轮说的名字）。

同等约 2 万 token 上下文下换模型的效果：

| 模型 | 首字 |
| --- | --- |
| `claude-opus-5-thinking-max` | 14.5s |
| `claude-sonnet-5-thinking-medium` | 6.6s |
| `claude-4.6-opus-max` | 4.9s |
| `claude-opus-5-thinking-medium` | 4.0s |

嫌慢就降推理档位，`-max` 换 `-medium` 大约快 3.6 倍。

### 慢模型与超时

有些模型在长的多轮工具对话里会先思考很久才吐第一个字。实测
`claude-fable-5-max` 处理一段带 7 轮工具结果的对话，**首字要 70 秒**，
期间上游每 10 秒发一次心跳。

代理按「有没有收到帧」区分两种等待：

- **一帧都没有**（连心跳都没有）→ 线路问题，`AGENT_FIRST_TOKEN_MS` 默认 60 秒后报错，
  文案指向出口节点。
- **心跳正常但还没吐字** → 模型在想，`AGENT_THINKING_MS` 默认 600 秒，
  超了才报错，并明确说明是思考太久而不是线路问题。

早期两者共用 60 秒，于是慢模型每次都在内容到达前 6 秒被掐断，
换号重试一遍还是同样结果，最终报 `all accounts failed`——看日志像是账号全挂了，
其实模型好好的，只是慢。

### 账号调度：失败只降权，不下线

失败的账号**默认不会被隔离或冷却**，只是排到调度末位，成功一次立刻恢复优先级；
降权本身也有 5 分钟有效期，一次偶发抖动不会长期压着某个账号。

这样改是因为「失败」这个信号并不可靠。上游抖动、出口区域限制、首字超时都会被算成
账号故障，按老逻辑攒够 3 次就把一个其实好用的账号隔离半小时——池子小的时候
直接变成「无可用账号」，而账号本身没有任何问题。

坏账号不会因此拖慢每个请求：它排在最后，只有前面的都占满并发才会轮到它；
单个请求内部还有故障转移，失败的账号会被排除后换号重试。
账号池页面会在状态下方显示「连败 N 次」并在悬停时给出最近的错误，
真废掉的号一眼能看出来。

账号很多、希望坏号彻底让路时，把时长配回去即可恢复老行为：

```bash
ACCOUNT_COOLDOWN_429_MS=60000    # 被限流后冷却
ACCOUNT_QUARANTINE_MS=1800000    # 连续失败达 ACCOUNT_MAX_FAILURES 次后隔离
```

### 关于 `usage`

Cursor 上游不返回 token 用量，所以这里由本地计算，见 `internal/tokenize`：

- 默认用真实 BPE 分词（[tiktoken-go/tokenizer](https://github.com/tiktoken-go/tokenizer)，
  纯 Go、词表内嵌、运行时不联网），按 model 名选编码，OpenAI 系模型结果与官方一致。
- Claude / Gemini 没有公开的官方分词器，用 `o200k_base` 近似。
- `TOKENIZER=estimate` 可关闭分词器改用启发式，省约 3～7MB 常驻内存。
- 完全不需要 token 计数时用 `./build.sh --lite`（即 `-tags notokenizer`）编译，
  二进制从 ~19MB 降回 ~8MB。

OpenAI 流式需要 usage 时按规范传 `stream_options: {"include_usage": true}`。

## 其它路由

- `/`：内置管理界面（SPA，未知路径回落 index.html）。
- `/api`：服务自身的 JSON 简介。`/health`：健康检查。
- `/admin/*`：账号 / Key / 验号 / 健康，受 `ADMIN_TOKEN` 保护。
- `/manage/*`：管理 REST API（界面与外部脚本共用），受 `ADMIN_TOKEN` 保护
  （`Authorization: Bearer` 或 `?token=`）。

## 目录结构（分层）

```
cmd/server            进程入口，只做装配与启动
internal/
  config              环境变量配置（惰性单例）
  store               JSON 持久化（内存缓存 + 读写锁 + 原子落盘）
  reqlog              请求日志环形缓冲
  types               跨层复用的消息结构
  proto               最小 protobuf 读写 + Chat/Agent 报文编解码
  httpx               出站 HTTP/2 客户端与代理出口管理
  cursor              指纹 / 请求头 / Chat 端点 / Agent 流 / 账号池 / 刷新 / 用量
  auth                Cursor 凭证 / 代理 Key / PKCE 换取 / 凭证解析
  openai              OpenAI & Anthropic 兼容处理器 + 故障转移管线
  vpn                 内置 Mihomo 机场管理
  tokenize            token 数量估算（usage 字段与日志统计）
  manage              账号/Key/模型/测试/VPN 的统一业务门面
  server              路由装配 + /manage REST API
  webui               go:embed 内嵌管理界面 + SPA 托管
webui/                Vue 3 + Tailwind + shadcn-vue 前端源码
  src/lib/api.ts        管理 API 客户端（含 SSE 流式）
  src/components/ui/    shadcn-vue 组件（按 DESIGN.md 改写了 button / badge 变体）
  src/components/app/   业务组件（AppShell / VibrantCard / SurfaceCard / CopyField …）
  src/views/            7 个页面
```

后端依赖只有 `github.com/google/uuid` 与 `golang.org/x/net`（HTTP/2），无重量级框架。

## 与 Node 版的差异

详见 [`OPTIMIZATION.md`](./OPTIMIZATION.md)。要点：

- 单一静态二进制（含管理界面），无 Node 运行时；内存/启动都更省。
- store 从「每次读盘」改为「内存缓存 + 读写锁」，热路径零磁盘 IO。
- 管理界面从 Electron 桌面窗口换成浏览器可访问的 WebUI，远程 / SSH 场景直接可用；
  底层是语言无关的 `/manage` REST API，不再绑定 Electron IPC。
- 移除了脆弱的 Playwright 无头浏览器批量登录（原代码自述 headless「基本过不去」人机校验），
  保留全部纯 HTTP 的登录/换取路径。
