// Package types 存放跨层复用的轻量数据结构。
package types

import "fmt"

// Message 是内部统一的对话消息（对齐 OpenAI/Anthropic 归一后的形态）。
// Content 允许是字符串或分块数组（[]any），由 ContentToText 拍平成纯文本。
type Message struct {
	Role    string
	Content any
}

// ContentToText 把可能为字符串 / 分块数组的消息内容拍平为纯文本。
func ContentToText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		out := ""
		for _, part := range v {
			switch p := part.(type) {
			case string:
				out += p
			case map[string]any:
				if t, ok := p["text"]; ok {
					out += fmt.Sprint(t)
				}
			}
		}
		return out
	default:
		return fmt.Sprint(v)
	}
}
