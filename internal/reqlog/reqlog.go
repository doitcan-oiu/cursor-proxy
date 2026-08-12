// Package reqlog 提供请求/流量日志的内存环形缓冲，供管理界面实时展示。
package reqlog

import (
	"sync"
	"time"
)

// Entry 一条请求日志。
type Entry struct {
	ID         int64  `json:"id"`
	Time       int64  `json:"time"`
	Kind       string `json:"kind"`
	Model      string `json:"model,omitempty"`
	Account    string `json:"account,omitempty"`
	KeyPrefix  string `json:"keyPrefix,omitempty"`
	Stream     bool   `json:"stream,omitempty"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Ms         int64  `json:"ms"`
	Chars      int    `json:"chars,omitempty"`
	// Tokens 是输出 token 的估算值（见 internal/tokenize）。
	Tokens int    `json:"tokens,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Stats 汇总统计。
type Stats struct {
	Total      int   `json:"total"`
	OK         int   `json:"ok"`
	Error      int   `json:"error"`
	ChatCount  int   `json:"chatCount"`
	TotalChars int   `json:"totalChars"`
	AvgMs      int64 `json:"avgMs"`
	LastMinute int   `json:"lastMinute"`
}

const maxEntries = 500

var (
	mu     sync.Mutex
	buffer []Entry
	seq    int64 = 1
)

// Record 追加一条日志（自动填充 id/time），返回完整条目。
func Record(e Entry) Entry {
	mu.Lock()
	defer mu.Unlock()
	e.ID = seq
	seq++
	e.Time = time.Now().UnixMilli()
	buffer = append(buffer, e)
	if len(buffer) > maxEntries {
		buffer = buffer[len(buffer)-maxEntries:]
	}
	return e
}

// List 返回 sinceID 之后的日志；sinceID<=0 时返回最近 200 条。
func List(sinceID int64) []Entry {
	mu.Lock()
	defer mu.Unlock()
	if sinceID > 0 {
		out := make([]Entry, 0)
		for _, e := range buffer {
			if e.ID > sinceID {
				out = append(out, e)
			}
		}
		return out
	}
	start := 0
	if len(buffer) > 200 {
		start = len(buffer) - 200
	}
	out := make([]Entry, len(buffer)-start)
	copy(out, buffer[start:])
	return out
}

// Clear 清空缓冲。
func Clear() {
	mu.Lock()
	defer mu.Unlock()
	buffer = nil
}

// Snapshot 计算当前统计信息。
func Snapshot() Stats {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now().UnixMilli()
	var s Stats
	s.Total = len(buffer)
	var sumMs int64
	for _, e := range buffer {
		if e.Status == "ok" {
			s.OK++
		}
		if e.Kind == "chat" {
			s.ChatCount++
		}
		s.TotalChars += e.Chars
		sumMs += e.Ms
		if now-e.Time < 60_000 {
			s.LastMinute++
		}
	}
	s.Error = s.Total - s.OK
	if s.Total > 0 {
		s.AvgMs = sumMs / int64(s.Total)
	}
	return s
}
