// Package types 存放跨层复用的轻量数据结构。
package types

import "fmt"

// Message 是内部统一的对话消息（对齐 OpenAI/Anthropic 归一后的形态）。
// Content 允许是字符串或分块数组（[]any），由 ContentToText 拍平成纯文本。
type Message struct {
	Role    string
	Content any
	// Images 是这条消息携带的图片。文本走 Content，图片单独走这里——
	// 上游协议里两者也是分开的字段，拍平成文本会丢掉图片。
	Images []Image
}

// Image 是一张待发给上游的图片。
type Image struct {
	Data     []byte
	MimeType string
	// Width / Height 解不出来时为 0，上游允许不带尺寸。
	Width  int
	Height int
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
