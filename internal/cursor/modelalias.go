package cursor

import "strings"

// aliases 把对外 model 名映射到上游实际 model 名。Cursor 没有 "auto"，对应 "default"。
var aliases = map[string]string{
	"auto":          "default",
	"gpt-4":         "gpt-4.1",
	"gpt-4o":        "gpt-4.1",
	"gpt-3.5-turbo": "default",
}

// ResolveUpstreamModel 把对外 model 名解析成上游 model 名，未知的原样透传。
func ResolveUpstreamModel(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if v, ok := aliases[key]; ok {
		return v
	}
	return strings.TrimSpace(model)
}
