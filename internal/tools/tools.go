// Package tools 用提示词模拟 function calling。
//
// 为什么要模拟：Cursor 的 agent 端点只认自己内置的那套工具，工具身份是
// protobuf 里写死的字段号（如字段 7 = 读文件），协议里没有任何位置能承载
// 客户端声明的具名函数与 JSON Schema。所以原生透传做不到。
//
// 做法是把客户端声明的工具写进提示词，约定模型用固定标签输出调用意图，
// 再从输出流里把这段标签剥离、还原成标准的 tool_calls。
package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// 模型输出工具调用时使用的标签。选用 XML 风格是因为主流模型对它的遵循度
// 明显好于裸 JSON，且不易与正文/代码块混淆。
const (
	OpenTag  = "<tool_call>"
	CloseTag = "</tool_call>"
)

// Definition 是客户端声明的一个可调用工具。
type Definition struct {
	Name        string
	Description string
	// Parameters 是 JSON Schema 原文（OpenAI 的 parameters / Anthropic 的 input_schema）。
	Parameters json.RawMessage
}

// Call 是从模型输出里解析出的一次工具调用。
type Call struct {
	ID   string
	Name string
	// Arguments 是 JSON 对象字符串，对齐 OpenAI 的 function.arguments。
	Arguments string
}

// Choice 表达客户端的 tool_choice 约束。
type Choice struct {
	// Mode 取 auto / none / required / function 之一。
	Mode string
	// Name 仅在 Mode == "function" 时有效。
	Name string
}

// BuildSystemPrompt 生成描述工具与输出协议的提示词段落。
// 无工具时返回空串，调用方据此决定是否注入。
func BuildSystemPrompt(defs []Definition, choice Choice) string {
	if len(defs) == 0 || choice.Mode == "none" {
		return ""
	}

	var b strings.Builder
	b.WriteString("# Tool calling\n\n")
	b.WriteString("You can call tools. To call one, emit a block in EXACTLY this format:\n\n")
	b.WriteString(OpenTag + "\n")
	b.WriteString(`{"name": "tool_name", "arguments": {"arg": "value"}}` + "\n")
	b.WriteString(CloseTag + "\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- The block content MUST be a single valid JSON object with keys \"name\" and \"arguments\".\n")
	b.WriteString("- \"arguments\" MUST be a JSON object matching the tool's schema (use {} if the tool takes none).\n")
	b.WriteString("- Emit one block per call; emit several blocks to call several tools.\n")
	b.WriteString("- Do NOT wrap the block in markdown code fences.\n")
	b.WriteString("- Results come back as messages from the \"tool\" role; then continue the task.\n")
	// 下面三条是关键：上游本身是个带原生工具的 agent，涉及文件/终端时它会倾向
	// 走自己的原生工具路径并直接结束轮次，导致只留下一句「我这就去做…」。
	// 必须明确否定它拥有内置能力，并禁止「宣告动作却不给出调用块」。
	b.WriteString("- You have NO built-in tools and NO direct access to files, the terminal, or the network.\n")
	b.WriteString("  Emitting a " + OpenTag + " block is the ONLY way you can take any action.\n")
	b.WriteString("- NEVER announce an action and then stop. If you say you will do something,\n")
	b.WriteString("  the SAME reply MUST contain the corresponding " + OpenTag + " block.\n")
	b.WriteString("- If a task needs several steps, call the first tool now; you will be invoked again\n")
	b.WriteString("  with the result and can continue from there.\n")
	// 上游的系统提示要求「编辑前必须先读文件」。创建新文件时这条规则会让模型
	// 反复去读一个不存在的文件，陷入死循环——必须显式豁免。
	b.WriteString("- If a read fails because the file does not exist, do NOT read it again.\n")
	b.WriteString("  The file simply needs to be created: call the write/create tool directly.\n")
	b.WriteString("- Never repeat a tool call that already failed with the same arguments.\n")
	b.WriteString("  Change your approach instead.\n")

	switch choice.Mode {
	case "required":
		b.WriteString("- You MUST call at least one tool in this turn.\n")
	case "function":
		fmt.Fprintf(&b, "- You MUST call the tool %q in this turn.\n", choice.Name)
	}

	b.WriteString("\n## Available tools\n")
	for _, d := range defs {
		fmt.Fprintf(&b, "\n### %s\n", d.Name)
		if d.Description != "" {
			b.WriteString(d.Description + "\n")
		}
		if len(d.Parameters) > 0 && string(d.Parameters) != "null" {
			b.WriteString("Parameters (JSON Schema):\n")
			b.WriteString(compactJSON(d.Parameters) + "\n")
		} else {
			b.WriteString("Parameters: none\n")
		}
	}
	return b.String()
}

func compactJSON(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return string(raw)
	}
	return out.String()
}

// Reminder 是追加到最后一条用户消息末尾的短提醒。
//
// 只放在 system 里不够：上游会在自己那 13KB 的 agent 提示词之后再拼接我们的内容，
// 我们的规则离生成点较远，容易被它的原生行为盖过。末尾提醒利用近因效应，
// 实测能显著降低「只宣告不调用」的概率。
func Reminder() string {
	return "\n\n[System reminder: you have no built-in tools. To act, emit a " +
		OpenTag + " block in this reply; describing an action without the block does nothing.]"
}

// RenderCall 把一次工具调用还原成提示词里的标签形式，
// 用于把历史里的 assistant tool_calls 回放给模型。
func RenderCall(c Call) string {
	args := strings.TrimSpace(c.Arguments)
	if args == "" {
		args = "{}"
	}
	return fmt.Sprintf("%s\n{\"name\": %q, \"arguments\": %s}\n%s", OpenTag, c.Name, args, CloseTag)
}

// NewCallID 生成 OpenAI 风格的调用 id。
func NewCallID() string {
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

// Scanner 是流式输出的工具调用解析器：
// 逐块喂入模型输出，返回可以安全展示的正文，同时把工具调用抽出来。
//
// 它会处理标签被切分到多个块的情况——例如上一块以 "<tool_" 结尾，
// 这部分会被暂存，不会被当作正文吐出去。
type Scanner struct {
	pending strings.Builder
	calls   []Call
	failed  int
}

// Push 喂入一段增量，返回其中可以直接输出的正文。
func (s *Scanner) Push(chunk string) string {
	s.pending.WriteString(chunk)
	return s.drain(false)
}

// Flush 收尾：吐出剩余正文。未闭合的标签会按原样返回，避免吞内容。
func (s *Scanner) Flush() string {
	return s.drain(true)
}

// Calls 返回已解析出的全部工具调用。
func (s *Scanner) Calls() []Call { return s.calls }

// Failed 返回格式非法、未能解析的调用块数量，用于日志排查。
func (s *Scanner) Failed() int { return s.failed }

func (s *Scanner) drain(final bool) string {
	var out strings.Builder
	buf := s.pending.String()

	for {
		open := strings.Index(buf, OpenTag)
		if open < 0 {
			break
		}
		rest := buf[open+len(OpenTag):]
		close := strings.Index(rest, CloseTag)
		if close < 0 {
			// 标签还没闭合：正文部分先吐出，其余等后续增量
			out.WriteString(buf[:open])
			buf = buf[open:]
			if final {
				// 收尾仍未闭合，原样归还，宁可多显示也不吞掉
				out.WriteString(buf)
				buf = ""
			}
			s.setPending(buf)
			return out.String()
		}
		out.WriteString(buf[:open])
		body := rest[:close]
		if c, ok := ParseCall(body); ok {
			s.calls = append(s.calls, c)
		} else {
			// 解析失败时原样归还整段，宁可让客户端看到一坨标签，
			// 也不能静默吞掉——那会变成一次彻底的空响应。
			s.failed++
			out.WriteString(OpenTag + body + CloseTag)
		}
		buf = rest[close+len(CloseTag):]
	}

	// 没有起始标签：把可能是标签前缀的尾巴留到下一块
	keep := 0
	if !final {
		keep = partialSuffixLen(buf, OpenTag)
	}
	out.WriteString(buf[:len(buf)-keep])
	s.setPending(buf[len(buf)-keep:])
	return out.String()
}

func (s *Scanner) setPending(v string) {
	s.pending.Reset()
	s.pending.WriteString(v)
}

// partialSuffixLen 返回 s 末尾有多少字符可能是 tag 的开头。
func partialSuffixLen(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(tag, s[len(s)-n:]) {
			return n
		}
	}
	return 0
}

// ParseCall 解析标签内的 JSON。容忍模型多加的 markdown 代码围栏。
func ParseCall(body string) (Call, bool) {
	text := strings.TrimSpace(body)
	text = stripFence(text)
	if text == "" {
		return Call{}, false
	}

	var raw struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		// 兼容模型偶尔写成 parameters / input
		Parameters json.RawMessage `json:"parameters"`
		Input      json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(text), &raw) != nil {
		// 模型有时会在 JSON 前后多写几句话，取最外层的一对花括号再试
		if inner := outermostObject(text); inner != "" {
			if json.Unmarshal([]byte(inner), &raw) != nil {
				return Call{}, false
			}
		} else {
			return Call{}, false
		}
	}
	if raw.Name == "" {
		return Call{}, false
	}

	args := raw.Arguments
	if len(args) == 0 {
		args = raw.Parameters
	}
	if len(args) == 0 {
		args = raw.Input
	}

	arguments := "{}"
	if len(args) > 0 {
		// arguments 也可能被写成 JSON 字符串，解包一层
		var asString string
		if json.Unmarshal(args, &asString) == nil {
			if strings.TrimSpace(asString) != "" {
				arguments = strings.TrimSpace(asString)
			}
		} else {
			arguments = compactJSON(args)
		}
	}

	return Call{ID: NewCallID(), Name: raw.Name, Arguments: arguments}, true
}

// outermostObject 取出最外层的一对花括号内容，用于剥掉模型多写的说明文字。
func outermostObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}
