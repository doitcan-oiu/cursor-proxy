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
| 请求日志 | 实时轮询、统计条、只看失败、错误详情 |

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

- 可靠性取决于模型是否遵守输出格式，并行多工具调用比原生能力弱。
- 标签解析失败时会把原文原样返回，不会静默吞掉内容。
- 请求里没有 `tools` 字段时完全不注入，普通对话的行为不受任何影响。

主要路径是**桥接上游的原生工具调用**。Cursor 的 agent 并不自己执行工具，而是把调用
下发给客户端执行；这些调用原先被整个忽略，表现就是「模型说要做某事，然后对话断了」。
现在会解析出来并按名称与 JSON Schema 映射到客户端声明的对应工具。已覆盖的内置工具：

| 上游工具 | 映射到客户端的 |
| --- | --- |
| 执行终端命令 | `bash` / `shell` / `run_terminal_cmd` … |
| 读文件 | `read` / `view_file` / `cat` … |
| 写文件 | `write` / `edit` / `create_file` … |
| 搜索 | `glob` / `grep` / `file_search` … |
| 派发子 agent | `task` / `agent` / `subagent` … |

文本协议作为补充，覆盖上游没有内置对应物的自定义工具。
用 `NATIVE_TOOL_BRIDGE=off` 可关闭桥接。

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
