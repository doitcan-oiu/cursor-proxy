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

### ~~P1：`usage`/`tokens` 计费恒为 0~~（已在 Go 版修复）
原版 `chat.ts` 的 `usage` 三个字段全部硬编码 0；`anthropic.ts` 更糟——`input_tokens` 恒为 0，
`output_tokens` 直接把**字符数**当 token 返回（英文约虚高 4 倍）。依赖 usage 做计费或限流的
下游（Cherry Studio、One API 之类）会直接算错。

Go 版新增 `internal/tokenize` 并接入两个端点：

- OpenAI 非流式返回真实的 `prompt_tokens` / `completion_tokens` / `total_tokens`；
  流式按规范支持 `stream_options.include_usage`，在 `[DONE]` 前补一个 `choices` 为空、
  只带 usage 的收尾分片。
- Anthropic 的 `message_start` 给 `input_tokens`，`message_delta` 给累计 `output_tokens`，
  非流式两者都给。
- 思考内容（reasoning）计入输出 token，与主流厂商的计费口径一致。

**计数方式**：默认用真实 BPE 分词器
（[tiktoken-go/tokenizer](https://github.com/tiktoken-go/tokenizer)，纯 Go 移植，
词表随二进制内嵌，运行时不联网），按 model 名选编码——`gpt-4o/4.1/4.5/5` 走 o200k_base，
`gpt-4/3.5` 走 cl100k_base，OpenAI 系模型的结果与官方完全一致。

Claude 与 Gemini 没有公开的官方分词器，这类模型用 o200k_base 近似。它同样是现代多语言
BPE，比按字符换算准得多，但仍是近似值——这一点在代码与文档里都写明了。

最初的实现只有启发式估算（CJK 约 1 字 1 token，其余约 4 字符 1 token）。实测下来它对
英文很准，但对中文误差明显：

| 样本 | cl100k 实际 | o200k 实际 | 启发式 |
| --- | --- | --- | --- |
| `Hello, how are you doing today?` | 8 | 8 | 8 |
| `你好，请用一句话介绍一下你自己。` | 18 | 10 | 16 |

o200k 对中文有大量多字合并 token，启发式会高估约 60%，所以改用了真实分词器。
启发式作为回退保留：分词器加载失败、`TOKENIZER=estimate`、或 `-tags notokenizer`
编译时启用。

**代价与取舍**：词表让二进制从 ~8MB 涨到 ~19MB，某个编码首次用到时加载约 3～7MB 常驻内存
（懒加载，用不到的编码永远不占）。单次编码约 8.6µs，可忽略。给了两个退路：运行期
`TOKENIZER=estimate` 只省内存，编译期 `-tags notokenizer` 连依赖一起去掉、体积回到 ~8MB。

> 附带修掉一个 Go 特有的坑：`len(string)` 返回字节数而非字符数，中文在 UTF-8 下每字 3 字节，
> 沿用原版写法会让中文输出再虚高 3 倍。现在长度统计一律用 `utf8.RuneCountInString`。

### P1：`decodeMessage` varint 用浮点累加，超大字段会丢精度
原 `protobuf.ts` 里 `result += (b & 0x7f) * Math.pow(2, shift)`，varint 超过 2^53 会丢精度。
虽然当前用到的字段（长度、role）都很小，但这是隐患。Go 版用标准库 `encoding/binary.Uvarint`
（uint64），从根上避免。

### ~~P1：流式对话结束后要空等 6 秒才收尾~~（已在 Go 版修复）
原版 `agent-client.ts` 判断「生成结束」的唯一依据是**空闲超时**（`AGENT_IDLE_MS`，默认 6 秒）：
内容不再流入、静默满 6 秒才认为结束。表现就是正文早已输出完，界面还要再转 6 秒。

用 `AGENT_FRAME_DEBUG=1` 抓真实帧序列后，上游行为才清楚：

```
1{13[0B]}                        心跳，固定每 10s 一个
4{3{...system...}} 4{1=1 ...}    开头：回写会话上下文
1{4{1="用户要求..."}}             生成：思考
1{1{1="答案是"}} 1{1{1="2。"}}    生成：正文增量
4{1=4 ...} 4{1=5 ...} 4{1=7 ...} 结尾：把整轮对话作为会话记录回写
1{13[0B]} 1{13[0B]} ...          之后只剩心跳，连接永不关闭
```

三个关键事实：**上游从不发 Connect 的 end-of-stream 帧**（信封 flag `0x02`），
**从不关闭连接**，而且**每 10 秒发一个心跳**。所以纯靠超时是唯一出路——这就是那 6 秒。

真正可用的结束信号是最后那批**会话记录回写帧**（顶层字段 4）：上游把整轮对话持久化，
就意味着这一轮结束了。注意开头也会回写一次上下文，所以必须限定「生成已经开始之后」
出现才算数。识别到之后把空闲窗口从 6s 缩到 `AGENT_FINISH_IDLE_MS`（默认 400ms）用于收残余帧；
若之后又来内容则撤回判定，回到正常等待。原来的 6s 退居为识别不到结束信号时的兜底。

真实账号实测：最后一个内容分片到 `[DONE]` 的间隔从 **6.019s 降到 0.403s**，
整体请求耗时从 8.8s 降到 2.3s。

顺带修掉两个相关问题：

- 心跳帧以前会被当作普通数据参与解析；现在显式识别并跳过。
- 一轮里只有思考内容、没有正文时（模型直接发起工具调用就会这样），`gotContent`
  始终为假，旧逻辑会一路等到首字超时（60s）再报错。现在按会话记录回写正常收尾。

`internal/cursor/agent_test.go` 用「发完记录仍不关闭、持续心跳」的假上游锁定了这些行为。

### ~~P0：请求伪造文件上下文，导致模型转去调用工具、回一句就截断~~（已在 Go 版修复）
原版 `agent-protobuf.ts` 把对话历史塞进一个**伪造的文件上下文**——ExplicitContext 里挂一个
`/context.txt`，内容是历史记录（没有历史时干脆填字符串 `"chat"`）。

后果是 Cursor 的 agent 认为自己身处一个代码工作区，于是先回一句
「先读取工作区规则，再按你的要求…」，接着发起一个读文件的工具调用就结束本轮。
纯对话代理既不转发也不执行工具调用，于是每轮都被截断成一句开场白。
接 OpenCode 这类 agent 客户端时表现为：回一句就停，得手动发「继续」，然后又停。

实测同一个提示词跑 5 次，**5 次全部截断**（0～32 字）：

```
先读取工作区规则，再按分点详细介绍 Go 的并发模型。
先查看工作区上下文，再按你的要求详细分点介绍 Go 的并发模型。
```

修法是把对话直接内联进消息正文，不再伪造文件上下文。这里有个坑：ExplicitContext
**不能整个省略**——省掉之后上游会照常接受请求、返回 ack，然后一个字都不生成，
直到首字超时。必须保留字段但传空上下文。改完后同样的提示词跑 4 次，
**4 次全部完整作答**（2556～3927 字）。

### ~~P0：写文件类回答不流式，长内容静默几十秒后整段弹出~~（已在 Go 版修复）
上游没有 ask 模式，纯对话里让它「写一段 SVG」也会走「写文件」调用。早期版本只解析
「调用完成」帧（`sm.2`），要等参数全部发完才能还原成正文，一个 7 千字的 SVG
要静默 57 秒再整段出现。

抓帧后发现上游其实是分片下发的，而且**片段是纯文本，不是残缺 JSON**——
不需要处理不完整 JSON 的边界情况，直接转发即可：

```
1{7 {1=调用id 2{12[0B]}}}            ← 进行中：工具类型已知，路径还空着
1{15{1=调用id 2{3{1="print(\"hello"}}}}  ← 内容片段
1{7 {1=调用id 2{12{1{1="/hello.py"}}}}}  ← 路径这时才到
1{15{1=调用id 2{3{1=" world\")"}}}}      ← 内容片段
1{2 {1=调用id 2{12{1{1=路径 6=完整内容}}}}} ← 调用完成
```

修法是解析 `sm.7`（拿工具类型与路径）与 `sm.15`（拿内容片段），
新增 `EventToolInputDelta` 事件，由 `internal/tools.LiveWriter` 边收边吐。
过程中踩到两个坑：

1. **故障转移缓冲把流式吃掉了**。`OpenWithFailover` 会缓存事件直到确认「这轮成功」，
   而判据里没有参数片段，于是几十个片段全被压在缓冲里，等调用结束才一次性放行——
   实测 78 个块的时间戳全是 57.0s。把非空参数片段也计入判据后才真正流起来。
2. **收尾要区分「已接管」和「没有余文」**。`Finish` 原先只返回字符串，
   散文类内容恰好以换行结尾时返回空串，调用方误以为没接管，又走一遍还原逻辑，
   同一份内容输出了两遍（18794 字 ≈ 2×9400）。改成额外返回 `handled` 标志。

改完后同一个 SVG：首块 3.8 秒到达，672 个块持续输出，全程 39.7 秒，内容不再重复。

### ~~P1：图片输入被静默丢弃，多模态完全不可用~~（已在 Go 版修复）
原版和 Go 早期版本都只从消息里抽文字：`ContentToText` 遍历分块时只取 `part["text"]`，
OpenAI 的 `image_url` 块与 Anthropic 的 `image` 块没有 `text`，于是被无声丢掉。
请求照常返回 200，模型回一句「我没有看到图片」——最难排查的那种失败。

修复要点是找到上游的图片通道。Cursor 客户端自带 `agent.v1` 的 protobuf 描述
（`resources/app/extensions/cursor-local-agent-runtime/dist/main.js`），
从中可以读到准确的字段号：

```
UserMessage.3      = SelectedContext        ← 早期版本这里发的是空字符串
SelectedContext.1  = repeated SelectedImage
SelectedImage.8    = bytes  data            ← 与 blob_id 同属一个 oneof，可内联
SelectedImage.7    = string mime_type
SelectedImage.4    = Dimension { 1: width, 2: height }
SelectedImage.2/3  = uuid / path
```

走内联 `data` 就不必先上传拿 blob。实测同一张图：修复前模型答「没有看到图片」，
修复后能准确念出图中的唯一编号、句子与图形颜色；发票截图能完整转成 JSON
（编号、4 行商品、小计、税、总计、到期日全对）。

同时修掉一个连带 bug：`injectToolPrompt` 追加工具说明时整个重建了 `types.Message`，
把新增的 `Images` 字段丢了。表现为「带图 + 声明工具」的请求里模型看不见图，
只回一句「我没有这个工具」。改成只改写 `Content` 字段后，
「读发票图 → 调用 record_invoice 工具」这条链路才跑通。

### ~~P1：以为上游没有 ask 模式，纯对话被迫走「写文件再还原」~~（已在 Go 版修复）
早期结论是「上游始终是 agent 形态，没有纯问答模式」，于是纯对话里问「写一段 SVG」
只能先接住写文件调用、再把内容还原成代码块。

从 Cursor 客户端自带的描述里可以看到这个结论是错的：

```
agent.v1.AgentMode = { 0:UNSPECIFIED, 1:AGENT, 2:ASK, 3:PLAN,
                       4:DEBUG, 5:TRIAGE, 6:PROJECT, 7:MULTITASK, 8:CUSTOM }
agent.v1.UserMessage.4 = mode
```

改为按客户端是否声明工具自动切换：没声明 `tools` 用 ASK（模型直接作答），
声明了用 AGENT（内置工具桥接照常）。实测同一个 SVG 提示词，ASK 模式下
472 块纯文本流式，不再绕道写文件；「写个 Python 快排」直接给代码块。

**后来又改回默认关闭**（`ASK_MODE=on` 才启用）。ASK 不是通用问答模式，而是 Cursor 里
「就代码库提问」的模式，上游会限定模型只回答代码相关问题。实测后果是模型拿它当拒绝理由：

> I'm in Ask mode, so I can't generate creative writing, roleplay content,
> or produce the interactive fiction output you're requesting.

更麻烦的是这句话进了对话历史后会被反复回放，模型会照着自己上一轮的说法继续拒绝，
于是整段对话就卡死在同一句回复上。而它换来的好处（省掉一层「写文件再还原」）
在流式修好之后已经不明显——实测同一批提示词在 agent 模式下 SVG 首块 2.7s、
1038 块流式，互动小说、通用问答都正常。

也试过用 `AgentRunRequest.8 = custom_system_prompt` 压掉那句自述，
上游返回 `invalid_argument`，这条路走不通。

### ~~P0：长回答写到一半被时长上限静默掐断~~（已在 Go 版修复）
`AGENT_HARD_CAP_MS` 默认 180 秒，且是从建流那一刻算起的墙钟时间，不看流是否还在正常输出。
长文写到 180 秒就被切断，而且切断时只发一个普通的 `EventEnd`，
客户端收到 `finish_reason=stop`——**半截回答看起来像正常说完**，这是最难排查的一类失败。

实测（`claude-4.6-opus-max`，一篇 1.5 万字技术长文）：

```
修复前：181.3s  finish_reason=stop  15744 字  结尾 "…典型配置 50ms × 3"   ← 断在词中间
修复后：553.1s  finish_reason=stop  48623 字  结尾 "…而是贯穿整个协议栈的系统工程。"
```

被掐断时输出仍在稳定流动（每 20 秒约 1450 字），完全不是卡住——说明这个上限
掐的是正常工作的流。卡住的流本来就由 `AGENT_IDLE_MS`（默认 6 秒）负责，
时长上限只该防「一直吐、永远不停」的失控流，所以默认值放宽到 1800 秒。

同时给 `EventEnd` 加了 `Truncated` 标记：真触发上限时报
`finish_reason=length`（Anthropic 侧 `stop_reason=max_tokens`）并打一行日志，
调试台会追加一句说明。截断可以接受，静默截断不行。

### ~~P1：内置工具的字段号靠抓帧猜，错了两个~~（已在 Go 版修复）
早期是抓帧观察结构再猜工具类型，一串 `if` 按顺序判断。用户导出的「未识别工具」
记录暴露了两个错误：字段 4 被当成「列出文件」（实际是 `glob`，`ls` 在 13），
字段 23 被当成「待办清单」（实际是 `ask_question`，`update_todos` 在 9）。
猜错不会报错，只会让工具被悄悄归错类。

改为从 Cursor 客户端自带的 protobuf 描述里抄
（`cursor-local-agent-runtime/dist/main.js` 的 `agent.v1.ToolCall`），
一次拿到全部 60 多个字段的规范名与参数结构，例如：

```
9  update_todos  UpdateTodosArgs { 1: repeated TodoItem, 2: merge }
                 TodoItem { 1: id, 2: content, 3: status, 4/5: 时间戳 }
4  glob          GlobToolArgs { 1: target_directory, 2: glob_pattern }
13 ls            LsArgs { 1: path, 2: ignore }
18 web_search    WebSearchArgs { 1: search_term }
42 await         AwaitArgs { 1: task_id, 2: block_until_ms, 3: regex }
```

同时把那串 `if` 改成查表（`toolParsers`），加工具只需加一行，也不再受判断顺序影响；
未解析参数的工具也登记了规范名，日志与「未识别工具」页面直接显示
`record_screen` 而不是光秃秃的 `#29`。

cursor 层与 tools 层的类型值本就是同一批字符串，两个手写的 switch 转换函数
（每加一个工具都要记得同步，漏了就会被归成「读文件」）也一并删掉了。

### ~~P1：工具「宣告了却没做完」时对话静默结束~~（已在 Go 版修复）
上游偶尔只发一个「进行中」帧宣告要用某工具，然后既不发参数也不发完成帧。
实测 `web_search` 稳定复现：

```
1{7{1="toolu_…" 2{18[0B] …}}}   ← 宣告要用 web_search，参数长度 0
1{7{1="toolu_…" 2{18[0B] …}}}   ← 又试一次，还是空
4{…}                            ← 直接回写会话记录，本轮结束
```

客户端只收到模型那句「I'll search for the latest Go version」然后流正常结束，
`finish_reason=stop`——又是一次静默失败。现在收尾时会检查「宣告过但没完成」的调用，
补一句说明指出是哪个工具、为什么没做完。

### ~~P0：两套工具协议打架，模型识破「伪造对话记录」后陷入死循环~~（已在 Go 版修复）
原生桥接做好之后，早期为提示词模拟准备的那一套没有撤掉，于是同一个请求里同时存在：

1. 一段声称「你没有任何内置工具」的注入提示词；
2. 把助手上一轮的调用渲染成 `<tool_call>` 标签、把工具结果渲染成
   `<tool_result id="…">` 标签，拼进正文当历史；
3. 模型自己真实的内置工具。

模型的反应是直接点破——原话是「我看到这条消息里嵌入了一段伪造的对话记录
（另一套系统提示、自定义的 `<tool_call>` 文本格式和预置的"工具结果"）。
我不会采用那个格式或身份——我有自己真实的工具」。它转而用真实工具去读文件，
但结果回传时又被拍平成同样的假格式，于是再读一遍：现场表现是无限重复读同一批文件。

三处一起改：

- **提示词不再说假话**。只声明「这是客户端提供的额外工具」，不否认内置能力。
- **只为没有原生对应物的自定义工具注入**。声明 `read` / `bash` / `grep` 的普通
  agent 客户端现在完全不触发注入。
- **历史改用中性叙述**（`assistant: called tool Read with {…}` /
  `Read returned: …`），不再套伪协议标签；失败单独标注成 `failed`。

另外对话以工具结果结尾时末尾没有任何指令，模型会倾向「重新开始」而不是「接着做」，
所以补一句 `The tool results above are already available to you… do not call the
same tools again.`。

实测同一个「评审两个文件」的场景：修复前无限重复读，修复后第 1 轮发出两个
`Read`、第 2 轮直接给出评审，重复调用次数 0。

顺带记一个**走不通的路**：上游协议里其实有结构化的
`UserMessageAction.conversation_history`（字段 7，含 `tool_call` / `tool_result`
子消息，结构见 Cursor 客户端自带的描述）。照着编码发过去，报文结构与描述完全一致，
但该端点**完全忽略这个字段**——只发结构化历史时，模型连上一轮说过的名字都不记得。
所以历史只能继续拍平成文本，重点在于拍得像正常上下文而不是像注入。

### ~~P1：账号动不动就被隔离，池子小的时候直接无号可用~~（已在 Go 版修复）
`ClassifyErrorMessage` 的兜底分支是 `OutcomeError`，也就是**任何认不出来的错误
都算账号故障**；`ClassifyStatus` 里 status 0（网络失败）与 5xx 同样如此。
连续 3 次就隔离 30 分钟。

问题在于这些「失败」多数与账号无关：上游抖动、首字超时、出口区域对某模型不可用、
网络瞬断，都会记到账号头上。实测用一个不通的出口打 5 次，账号就被判死刑，
而它本身完全正常。池子小的时候表现为「跑几次就没号可用了」。

改成**只降权不下线**：失败只记连败次数用于排序，最近失败过的账号排到调度末位但
仍然可选，成功一次立刻清零；降权有 5 分钟有效期，避免一次抖动长期压着某个账号。
`ACCOUNT_COOLDOWN_429_MS` 与 `ACCOUNT_QUARANTINE_MS` 默认改为 0（关闭），
账号很多、希望坏号彻底让路时可以配回去。

坏账号不会因此拖慢每个请求：它排最后，且单请求内的故障转移会把它排除后换号重试。
账号池页面在状态下方显示「连败 N 次」并在悬停时给出最近错误，真废掉的号一眼能看出来。

### ~~P0：慢模型在内容到达前被掐断，报成「所有账号都失败」~~（已在 Go 版修复）
首字超时（`AGENT_FIRST_TOKEN_MS`，默认 60s）不区分「线路不通」和「模型在慢慢想」，
两种情况共用同一个预算。抓帧看真实时间线：

```
 2.4s  最后一个内容帧（会话回写）
12.3s  心跳 1{13[0B]}
22.3s  心跳
32.3s / 42.3s / 52.3s / 62.5s  心跳
66.2s  正文终于来了："I'll verify the actual files on disk…"
```

`claude-fable-5-max` 处理带 7 轮工具结果的对话时思考了 64 秒，期间上游每 10 秒
发一次心跳——连接完全正常。代理在第 60 秒掐掉、换号重试，第二个账号同样在 60 秒
被掐，最后报 `all accounts failed: 上游 60s 未返回任何内容：出口节点可能不通…`，
耗时 133 秒。从日志看像是账号全挂了，实际上内容再等 6 秒就到了。

改成按「有没有收到帧」分流：一帧都没有才按线路问题处理（仍是 60s）；
收到过心跳就说明模型在想，改用 `AGENT_THINKING_MS`（默认 600s），
超时文案也明确说明是思考太久、连接正常。修复后同一个请求首字 70.5s 到达并正常完成。

### ~~P1：待办清单调用被客户端 schema 校验打回~~（已在 Go 版修复）
把上游的 `update_todos` 映射成客户端工具时，参数写死成 `id/content/status`。
接 OpenCode 实测每次都失败：

```
✗ Todos failed
Error: The todowrite tool was called with invalid arguments:
  SchemaError(Missing key at ["todos"][0]["priority"]).
```

抓它发来的 schema 才看清，`content` / `status` / `priority` 三个全是必填，
而且根本没有 `id` 字段。各家客户端要求还不一样——Claude Code 的 `TodoWrite`
要的是 `activeForm`。写死任何一套都会在别家翻车。

改成读取工具的 items schema 按需生成：只填它声明过的字段，
`priority` 这类上游没有的给中性默认值，认不出来但必填的按类型补占位值，
schema 缺失时退回到通用三字段。改完后 OpenCode 里待办正常渲染，不再报错。

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
