package cursor

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cursor-proxy/internal/proto"
)

// AGENT_FRAME_DEBUG=1 时打印上游每一帧的 protobuf 字段结构。
// Cursor 的 agent 协议没有公开文档，全靠逆向；上游改动协议时用这个开关
// 可以直接看清帧序列，是排查「流为什么不结束 / 内容为什么丢」的主要手段。
var frameDebug = os.Getenv("AGENT_FRAME_DEBUG") != ""

var debugStart = time.Now()

// describeDeep 递归展开 protobuf 字段结构，varint 显示取值，短文本直接显示。
func describeDeep(data []byte, depth int) string {
	fields := proto.Decode(data)
	if len(fields) == 0 {
		return ""
	}
	keys := make([]int, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, f := range fields[k] {
			switch {
			case f.WireType == 0:
				parts = append(parts, fmt.Sprintf("%d=%d", k, f.Varint))
			case depth > 0 && len(f.Bytes) > 0:
				if s := string(f.Bytes); utf8.ValidString(s) && isPrintable(s) {
					parts = append(parts, fmt.Sprintf("%d=%q", k, truncStr(s, 40)))
					continue
				}
				if inner := describeDeep(f.Bytes, depth-1); inner != "" {
					parts = append(parts, fmt.Sprintf("%d{%s}", k, inner))
					continue
				}
				parts = append(parts, fmt.Sprintf("%d[%dB]", k, len(f.Bytes)))
			default:
				parts = append(parts, fmt.Sprintf("%d[%dB]", k, len(f.Bytes)))
			}
		}
	}
	return strings.Join(parts, " ")
}

func isPrintable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func truncStr(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cnt := 0
	for i := range s {
		cnt++
		if cnt > n {
			return s[:i] + "…"
		}
	}
	return s
}

func debugFrame(flag byte, length uint32, payload []byte) {
	log.Printf("[frame %6.3fs] flag=%#02x len=%-5d %s",
		time.Since(debugStart).Seconds(), flag, length, describeDeep(payload, 6))
}
