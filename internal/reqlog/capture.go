package reqlog

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxRequestBytes 是单条日志里保留的请求体上限。
//
// 真实的 agent 会话很容易到几十 KB：一次代码排查跑下来带七八轮工具结果，
// 实测 62KB。早期定的 16KB 会把它截成不可解析的半截 JSON，拿到手也没法直接复现。
// 图片载荷已经单独剥掉了，所以这里可以放宽。
const MaxRequestBytes = 128 << 10

// maxBodies 是最多为多少条错误保留请求体。
// 上游整体不可用时错误会成片出现，全留会把内存吃掉；只留最近这些条即可。
// 与 MaxRequestBytes 相乘就是最坏内存占用（约 2.5MB）。
const maxBodies = 20

// reDataURL 匹配内嵌图片的 base64 载荷。
// 一张图就能有几百 KB，原样留下会把请求体撑爆，而且对排查没帮助——
// 知道「这里有一张多大的 png」就够了。
var reDataURL = regexp.MustCompile(`"data:([a-zA-Z0-9./+-]*);base64,[A-Za-z0-9+/=\s]{64,}"`)

// SanitizeRequest 把原始请求体整理成适合留档的形态：
// 去掉内嵌图片的 base64 载荷，并限制总长度。
func SanitizeRequest(raw []byte) string {
	s := reDataURL.ReplaceAllStringFunc(string(raw), func(m string) string {
		sub := reDataURL.FindStringSubmatch(m)
		mime := "image"
		if len(sub) > 1 && sub[1] != "" {
			mime = sub[1]
		}
		// 减去 data URL 前缀与两个引号，粗略还原载荷长度
		return fmt.Sprintf("%q", fmt.Sprintf("<%s base64 已省略，约 %d KB>", mime, len(m)/1024))
	})

	if len(s) > MaxRequestBytes {
		s = s[:MaxRequestBytes] + fmt.Sprintf("\n…（已截断，原始长度 %d 字节）", len(raw))
	}
	return strings.TrimSpace(s)
}

// trimBodies 只为最近若干条错误保留请求体，更早的清掉。
// 调用方必须已持有 mu。
func trimBodies() {
	kept := 0
	for i := len(buffer) - 1; i >= 0; i-- {
		if buffer[i].Request == "" {
			continue
		}
		kept++
		if kept > maxBodies {
			buffer[i].Request = ""
		}
	}
}
