package proto

import (
	"strings"
	"testing"

	"cursor-proxy/internal/types"
)

func TestBuildPromptSingleUserMessageIsVerbatim(t *testing.T) {
	got := BuildPrompt([]types.Message{{Role: "user", Content: "你好"}})
	if got != "你好" {
		t.Fatalf("单条 user 消息应原样送出，得到 %q", got)
	}
}

func TestBuildPromptSystemHasNoRolePrefix(t *testing.T) {
	got := BuildPrompt([]types.Message{
		{Role: "system", Content: "你是一个简洁的助手。"},
		{Role: "user", Content: "你好"},
	})
	if !strings.HasPrefix(got, "你是一个简洁的助手。") {
		t.Fatalf("系统提示应作为正文开头且不带角色前缀，得到 %q", got)
	}
	if !strings.Contains(got, "user: 你好") {
		t.Fatalf("用户消息应带角色前缀，得到 %q", got)
	}
}

func TestBuildPromptKeepsMultiTurnOrder(t *testing.T) {
	got := BuildPrompt([]types.Message{
		{Role: "user", Content: "我养了一只叫豆豆的猫"},
		{Role: "assistant", Content: "记住了"},
		{Role: "user", Content: "它叫什么"},
	})
	iFirst := strings.Index(got, "豆豆")
	iMid := strings.Index(got, "记住了")
	iLast := strings.Index(got, "它叫什么")
	if iFirst < 0 || iMid < 0 || iLast < 0 {
		t.Fatalf("多轮内容应全部保留，得到 %q", got)
	}
	if !(iFirst < iMid && iMid < iLast) {
		t.Fatalf("多轮顺序应保持原样，得到 %q", got)
	}
}

func TestBuildPromptSkipsEmptyContent(t *testing.T) {
	got := BuildPrompt([]types.Message{
		{Role: "system", Content: "   "},
		{Role: "user", Content: "在吗"},
	})
	if got != "user: 在吗" {
		t.Fatalf("空内容应被跳过，得到 %q", got)
	}
}

// 请求里必须带 ExplicitContext（哪怕是空的）：完全省略该字段时上游不生成任何内容。
// 同时不能再伪造 /context.txt 文件上下文，否则 agent 会转去调用工具读工作区。
func TestEncodeAgentRequestHasEmptyExplicitContext(t *testing.T) {
	raw := EncodeAgentRequest([]types.Message{{Role: "user", Content: "hi"}}, "auto", ModeAgent)

	if strings.Contains(string(raw), "/context.txt") {
		t.Fatal("不应再伪造文件上下文 /context.txt")
	}

	clientMsg := Decode(raw)
	runReq := Decode(FirstBytes(clientMsg, 1))
	convAction := Decode(FirstBytes(runReq, 2))
	userAction := Decode(FirstBytes(convAction, 1))

	ctxField, ok := userAction[2]
	if !ok {
		t.Fatal("UserMessageAction 缺少 ExplicitContext 字段")
	}
	if len(ctxField[0].Bytes) != 0 {
		t.Fatalf("ExplicitContext 应为空，实际 %d 字节", len(ctxField[0].Bytes))
	}

	if text := FirstString(Decode(FirstBytes(userAction, 1)), 1); text != "hi" {
		t.Fatalf("消息正文 = %q，期望 %q", text, "hi")
	}
}

// 图片挂在 UserMessage.selected_context 里，字段号取自 Cursor 客户端自带的
// agent.v1 描述。挂错位置上游不会报错，只是模型看不到图——必须锁死结构。
func TestEncodeAgentRequestCarriesImages(t *testing.T) {
	msgs := []types.Message{{
		Role:    "user",
		Content: "这张图里有什么",
		Images: []types.Image{
			{Data: []byte{1, 2, 3}, MimeType: "image/png", Width: 40, Height: 25},
			{Data: []byte{9, 9}, MimeType: "image/jpeg"},
		},
	}}

	top := Decode(EncodeAgentRequest(msgs, "auto", ModeAgent))
	runReq := Decode(FirstBytes(top, 1))
	action := Decode(FirstBytes(runReq, 2))
	userAction := Decode(FirstBytes(action, 1))
	userMsg := Decode(FirstBytes(userAction, 1))

	if got := FirstString(userMsg, 1); got != "这张图里有什么" {
		t.Fatalf("正文应照常发送，实际 %q", got)
	}

	selected := Decode(FirstBytes(userMsg, 3))
	imgs := selected[1]
	if len(imgs) != 2 {
		t.Fatalf("应挂上 2 张图，实际 %d", len(imgs))
	}

	first := Decode(imgs[0].Bytes)
	if got := FirstBytes(first, 8); string(got) != "\x01\x02\x03" {
		t.Fatalf("字段 8 应是图片字节，实际 %q", got)
	}
	if got := FirstString(first, 7); got != "image/png" {
		t.Fatalf("字段 7 应是 MIME，实际 %q", got)
	}
	if got := FirstString(first, 2); got == "" {
		t.Fatal("字段 2 应有 uuid")
	}
	dim := Decode(FirstBytes(first, 4))
	if dim[1][0].Varint != 40 || dim[2][0].Varint != 25 {
		t.Fatalf("字段 4 应是宽高，实际 %d x %d", dim[1][0].Varint, dim[2][0].Varint)
	}

	// 没有尺寸的图不写 dimension，但字节和 MIME 照发
	second := Decode(imgs[1].Bytes)
	if FirstBytes(second, 4) != nil {
		t.Fatal("尺寸未知时不应写 dimension")
	}
	if string(FirstBytes(second, 8)) != "\x09\x09" {
		t.Fatal("第二张图的字节应保留")
	}
}

// 没有图片时结构要和以前一致：字段 3 存在但为空。
// 早期版本这里发的是空字符串，改成空消息后行为等价，别回退成不发。
func TestEncodeAgentRequestWithoutImages(t *testing.T) {
	top := Decode(EncodeAgentRequest([]types.Message{{Role: "user", Content: "hi"}}, "auto", ModeAgent))
	runReq := Decode(FirstBytes(top, 1))
	action := Decode(FirstBytes(runReq, 2))
	userAction := Decode(FirstBytes(action, 1))
	userMsg := Decode(FirstBytes(userAction, 1))

	if _, ok := userMsg[3]; !ok {
		t.Fatal("字段 3 应存在")
	}
	if len(FirstBytes(userMsg, 3)) != 0 {
		t.Fatal("没有图片时字段 3 应为空")
	}
}

// mode 决定上游按 agent 还是纯问答行事。纯对话用 ASK 能让模型直接作答，
// 不再把内容塞进「写文件」调用；声明了工具时必须留在 agent 模式。
func TestEncodeAgentRequestMode(t *testing.T) {
	userMessageOf := func(raw []byte) map[int][]Field {
		top := Decode(raw)
		runReq := Decode(FirstBytes(top, 1))
		action := Decode(FirstBytes(runReq, 2))
		userAction := Decode(FirstBytes(action, 1))
		return Decode(FirstBytes(userAction, 1))
	}
	msgs := []types.Message{{Role: "user", Content: "hi"}}

	for _, tc := range []struct {
		mode Mode
		want uint64
	}{{ModeAsk, 2}, {ModeAgent, 1}} {
		um := userMessageOf(EncodeAgentRequest(msgs, "auto", tc.mode))
		if len(um[4]) == 0 || um[4][0].Varint != tc.want {
			t.Fatalf("mode %d 应写入字段 4 的值 %d，实际 %+v", tc.mode, tc.want, um[4])
		}
	}

	// 不指定时不写该字段，保持上游默认行为
	if um := userMessageOf(EncodeAgentRequest(msgs, "auto", ModeUnspecified)); len(um[4]) != 0 {
		t.Fatal("未指定模式时不应写入字段 4")
	}
}

// 历史里的工具调用与结果要用平铺叙述，不能套 <tool_call> 之类的伪协议标签。
// 模型会把那种写法判定为「伪造的代理对话记录」并拒绝采信，
// 转而反复重试同一个工具——现场表现就是无限重复读同一批文件。
func TestBuildPromptRendersToolTurnsPlainly(t *testing.T) {
	got := BuildPrompt([]types.Message{
		{Role: "system", Content: "你是代码评审助手。"},
		{Role: "user", Content: "评审这两个文件"},
		{Role: "assistant", Content: "我先读一下。", ToolCalls: []types.ToolCall{
			{ID: "c1", Name: "Read", Args: `{"filePath":"/a.py"}`},
		}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Read", Content: "print(1)"},
	})

	for _, banned := range []string{"<tool_call>", "</tool_call>", "<tool_result", "<tool_error"} {
		if strings.Contains(got, banned) {
			t.Fatalf("不应出现伪协议标签 %q：\n%s", banned, got)
		}
	}
	for _, want := range []string{"你是代码评审助手。", "评审这两个文件", "我先读一下。",
		"called tool Read", `{"filePath":"/a.py"}`, "Read returned", "print(1)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示词里应包含 %q：\n%s", want, got)
		}
	}
}

// 对话以工具结果结尾时末尾没有任何指令，模型会倾向「重新开始」而不是「接着做」，
// 表现为把刚执行过的工具再调一遍。必须补一句继续指令。
func TestBuildPromptAsksToContinueAfterToolResult(t *testing.T) {
	got := BuildPrompt([]types.Message{
		{Role: "user", Content: "读一下"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "c1", Name: "Read", Args: "{}"}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "Read", Content: "内容"},
	})
	if !strings.Contains(got, "do not call the same tools again") {
		t.Fatalf("末尾应提示不要重复调用：\n%s", got)
	}

	// 以用户提问结尾时不该加这句，否则会干扰正常提问
	normal := BuildPrompt([]types.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好"},
		{Role: "user", Content: "再见"},
	})
	if strings.Contains(normal, "do not call the same tools again") {
		t.Fatalf("普通对话不该出现继续指令：\n%s", normal)
	}
}

// 工具失败要标注出来，否则模型看不出上一次已经失败，会原样再试一遍。
func TestBuildPromptMarksFailedTool(t *testing.T) {
	got := BuildPrompt([]types.Message{
		{Role: "user", Content: "读一下"},
		{Role: "tool", ToolName: "Read", Content: "no such file", IsError: true},
	})
	if !strings.Contains(got, "Read failed") {
		t.Fatalf("失败应单独标注：\n%s", got)
	}
}

// 单条用户消息仍要原样送出，不加任何包装。
func TestBuildPromptSingleUserMessageUnchanged(t *testing.T) {
	if got := BuildPrompt([]types.Message{{Role: "user", Content: "你好"}}); got != "你好" {
		t.Fatalf("单条消息应原样送出，实际 %q", got)
	}
}
