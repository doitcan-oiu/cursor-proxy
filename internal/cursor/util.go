package cursor

import "net/url"

// urlEncode 近似 JS encodeURIComponent（JWT 只含 base64url 字符与点，行为一致）。
func urlEncode(s string) string {
	return url.QueryEscape(s)
}
