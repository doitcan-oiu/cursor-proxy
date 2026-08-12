# 改写说明与优化分析

本文件记录把原 Node + Electron 项目改写为 Go 时的分层设计，以及改写过程中发现的
**原项目值得优化的地方**（含已在 Go 版落地的、以及建议后续跟进的）。

## 一、分层设计

改写遵循「自下而上、单向依赖」原则，杜绝循环依赖：

```
config ─┐
store ──┼─ auth ─┐
reqlog  │        ├─ cursor ─ openai ─┐
types ──┘        │                   ├─ server ─ cmd/server
httpx ───────────┘        manage ────┘
proto ───────────────────────────────
vpn ── (被 manage / server 使用)
```

- **基础层**：`config`（配置）、`store`（持久化）、`reqlog`（日志）、`types`（消息模型）、
  `proto`（protobuf 编解码）、`httpx`（出站客户端）。互不依赖或只依赖 config。
- **领域层**：`auth`（凭证与 Key）、`cursor`（上游协议、账号池、刷新、用量）。
- **协议层**：`openai`（OpenAI/Anthropic 兼容处理器 + 故障转移管线）。
- **门面/装配层**：`manage`（业务门面）、`server`（路由）、`cmd/server`（入口）。

对应原项目的问题：原代码里 `manage.ts` 直接引用了十几个模块、`app.ts` 混合了鉴权中间件与
业务路由、`connect.ts` 同时承担「信封编解码」和「dispatcher 缓存」两件事。Go 版按职责拆开，
每个包只有一个清晰理由存在。

## 二、已在 Go 版落地的优化

### 1. store：从「每次读盘」改为「内存缓存 + 读写锁」
原 `store.ts` 注释写道「每次都从磁盘读取……数据量很小，开销可忽略」。但 store 在**热路径**上被
反复调用：每次 `validateProxyKey`、每次 `acquireAccount`（选号循环里还会多次读）、每次
`healthSnapshot`。高并发下这是每请求多次 `readFileSync + JSON.parse`。

Go 版用 `sync.RWMutex` 守护一份内存快照，只有写操作才落盘（写临时文件 + `rename` 原子替换）。
读路径零磁盘 IO，写路径仍然崩溃安全。

> 代价：放弃了原版「多进程（CLI + 服务）共享同一份磁盘状态」的隐性能力。原项目靠读盘让另一个
> 进程能看到写入。Go 版是单进程服务，这个取舍是合理的；若未来要多进程，应换成 SQLite 或加文件
> 监听，而不是退回每次读盘。

### 2. 账号池调度的并发安全
原 `account-manager.ts` 的 `acquireAccount` 在单线程事件循环里「读健康 → 选号 → `inFlight++`」
天然原子。Go 是真并发，若照搬会有 TOCTOU 竞态：两个请求可能同时选中同一个刚好在阈值边缘的账号。
Go 版把「筛选 + 预占自增」放在同一把互斥锁内完成，消除竞态。

### 3. HTTP 客户端复用与出口优先级
沿用原版「按代理 URL 缓存 dispatcher」的思路，用 `map[string]*http.Client` 缓存，
每个 client 自带连接池（`MaxIdleConnsPerHost`）并强制 HTTP/2（Cursor 的 chat 要求 h2）。
出口优先级同原版：账号独立代理 > 内置 VPN > `CURSOR_HTTP_PROXY` > 直连。

### 4. 流式对话的资源释放更确定
原版用异步生成器 + `Promise.race` 做空闲判定，取消时靠 `gen.return()`。Go 版用
`context.CancelFunc` + channel，`AgentStream.Close()` 会取消底层请求、终止读协程、关闭 body，
避免坏节点把连接与 goroutine 泄漏。故障转移探帧后若换号，明确 `Close()` 旧流。

### 5. 管理层与 GUI 解耦
原版管理能力绑死在 Electron IPC（`ipcMain.handle` + `preload.ts`）。Go 版把同样的能力抽象成
语言无关的 `/manage/*` REST API，任何前端（浏览器、脚本、其它面板）都能用，且和对外 `/v1`
在同一个进程/端口，省掉 Electron 主进程这一层。

### 6. Electron 桌面窗口 → 浏览器 WebUI
原版管理界面只能在装了图形环境的本机以 Electron 窗口打开。这对一个**常驻服务端程序**是错配：
反代通常跑在 VPS / NAS / 容器里，那里既没有桌面环境，也不该为了看一眼账号状态装一套 Chromium。
原版在这种环境下只能退化成 curl 敲 `/admin`。

Go 版改成 `go:embed` 内嵌的浏览器管理界面：
- **零额外部署**：界面打进同一个二进制，没有单独的静态目录要伺候，`scp` 一个文件就能跑。
- **远程可用**：SSH 端口转发或直接开放端口即可在任意机器的浏览器操作。
- **不依赖外网**：DM Sans 字体走 npm 自托管打包，内网/离线环境样式不会退化。
- **安全边界明确**：界面与 `/manage/*` 共用 `ADMIN_TOKEN`，口令只存浏览器 localStorage；
  静态资源用内容哈希长缓存，`index.html` 不缓存。

技术栈 Vue 3 + Tailwind CSS v4 + shadcn-vue，视觉严格实现 `DESIGN.md` 的设计系统。
shadcn-vue 的「源码归你所有」模型正好用上了——`button` / `badge` 的变体被直接改写为设计稿
要求的药丸形与语义色，而不是在外面套一层覆盖样式。

顺带补了一个后端能力：`POST /manage/chat/test-stream`（SSE），让调试台能像真实客户端那样
边生成边显示，而不是原版那种等全部生成完才一次性返回。

## 三、改写中发现的、原项目值得优化的点（建议）

以下是阅读原代码时记录的问题，按优先级排列。部分需要产品决策，未擅自在 Go 版实现。

### P0：token / 明文凭证落盘为明文
`store.json` 直接明文保存 `WorkosCursorSessionToken` / accessToken / refreshToken，文件权限
`0600` 只挡了同机其它用户。这些凭证等价于账号登录态。建议：用一个主密钥（来自环境变量或 OS
keychain）对 token 字段做对称加密后再落盘。Go 版沿用了明文以保持行为一致，但这是**首要**安全项。

### P1：`usage`/`tokens` 计费恒为 0
`chat.ts` / `anthropic.ts` 里 `usage` 全部硬编码 0，`output_tokens` 直接用字符数充数。对接
需要按量计费或限流的下游会失真。建议接一个 tokenizer（如 tiktoken 的 Go 实现）估算，或至少对
字符数做一个粗略换算并标注为估算值。

### P1：`decodeMessage` varint 用浮点累加，超大字段会丢精度
原 `protobuf.ts` 里 `result += (b & 0x7f) * Math.pow(2, shift)`，varint 超过 2^53 会丢精度。
虽然当前用到的字段（长度、role）都很小，但这是隐患。Go 版用标准库 `encoding/binary.Uvarint`
（uint64），从根上避免。

### P2：`checkAllAccounts` / 批量验号会打真实计费请求
`get-current-period-usage` 和 `full_stripe_profile` 每次验号都会请求 Cursor 官方接口。账号多时
一轮全量验号是几十上百个外部请求，且无缓存。建议对 plan/usage 结果做短 TTL 缓存（如 5 分钟），
并对整体验号加频率限制。

### P2：错误分类依赖正则匹配英文文案，易随上游改动失效
`classifyErrorMessage` / `friendlyUpstream` 用一堆正则匹配 Cursor 返回的错误字符串。上游一旦改文案
就会误判（把「该换号」当成「别重试」或反之）。建议优先用结构化字段（HTTP status、错误 `code`）
分类，正则只作兜底。Go 版保留了同样的正则以对齐行为，但结构上已把 status 分类和 message 分类分开。

### P2：账号选择是「读快照 + 排序」，账号极多时是 O(n log n)/请求
当前账号规模下没问题。若账号数达到数千，`acquireAccount` 每轮对全量账号排序会成为瓶颈。建议改为
维护一个按 `inFlight`/`lastStartAt` 排序的堆或分桶结构增量更新。

### P3：Playwright 无头浏览器批量登录（已在 Go 版移除）
原 `login-browser.ts` 自述：headless 模式下人机校验「基本过不去」，需要有头手动点。这个功能：
- 依赖庞大（Playwright + 浏览器内核几百 MB）；
- 成功率低且脆弱（选择器随 Cursor 登录页改版就失效，代码里已堆了大量兜底选择器）；
- 与「无人值守服务」定位冲突（要人工过验证码）。

Go 版**不移植**它，改为保留全部**纯 HTTP**的稳定路径：直接导入 token、批量导入、以及
deep-control PKCE 用 web token 换 session token（`/admin/cursor-tokens` 会自动识别 web token 并
换取）。配套的 IMAP 接码（`imap-code.ts`）也随之移除，因为它只服务于浏览器登录流程。
若确需自动化注册/登录，建议独立成一个可选的外部工具，而非塞进核心反代服务。

### P3：`_agent2.ts`、`start.bat`、`claude-code.bat` 等根目录散落脚本
原项目根目录有实验性/临时脚本混在正式代码旁。Go 版目录结构清爽，建议原项目也把这些挪进
`scripts/` 或删除。

## 四、未改变的行为（刻意对齐）

为保证 Go 版是「等价替换」，以下刻意与原版保持一致，即使它们本身有可优化空间：
- 设备指纹派生算法（`sha256(bearer+salt)`、`uuidv5`）、`x-cursor-checksum` 混淆算法。
- protobuf 字段号与报文结构（Chat 与 Agent 两套）。
- 防封调度的默认参数（并发 64、429 冷却 60s、隔离 30min 等）。
- 错误文案的中文提示与故障转移的重试判定。

这样已有的账号指纹、已签发的 Key、下游的对接方式都无需改动即可迁移。
