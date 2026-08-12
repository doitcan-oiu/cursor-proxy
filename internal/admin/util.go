package admin

import (
	"regexp"
	"strings"
)

var bearerRe = regexp.MustCompile(`(?i)^Bearer\s+`)

func stripBearer(s string) string {
	return strings.TrimSpace(bearerRe.ReplaceAllString(s, ""))
}
