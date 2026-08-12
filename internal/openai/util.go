package openai

import (
	"regexp"
	"strings"
)

var bearerRe = regexp.MustCompile(`(?i)^Bearer\s+`)

// stripBearer 去掉 Authorization 头里的 Bearer 前缀。
func stripBearer(s string) string {
	return strings.TrimSpace(bearerRe.ReplaceAllString(s, ""))
}
