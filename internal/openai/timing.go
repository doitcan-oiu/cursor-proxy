package openai

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// PROXY_TIMING=1 时打印每个请求各阶段的耗时。
//
// 排查「感觉很慢」时，第一步得先分清慢在哪一段：是我们自己的编解码、
// 取号与刷 token，还是上游在想。只看总耗时永远只能猜。
var timingEnabled = os.Getenv("PROXY_TIMING") != ""

// timeline 记录一次请求内各阶段的时间点。
type timeline struct {
	label  string
	start  time.Time
	last   time.Time
	stages []stage
}

type stage struct {
	name string
	d    time.Duration
}

func newTimeline(label string) *timeline {
	if !timingEnabled {
		return nil
	}
	now := time.Now()
	return &timeline{label: label, start: now, last: now}
}

// mark 记下从上一个标记到现在的耗时。
func (t *timeline) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.stages = append(t.stages, stage{name, now.Sub(t.last)})
	t.last = now
}

// markOpen 把「取号 / 连接 / 等上游确认有产出」三段分别记下来。
// 只标一个「取号+建流」看不出到底卡在刷 token、握手还是上游思考。
func (t *timeline) markOpen(o *OpenedStream) {
	if t == nil {
		return
	}
	if o == nil {
		t.mark("建流失败")
		return
	}
	t.stages = append(t.stages,
		stage{"取号(含刷token)", time.Duration(o.AcquireMs) * time.Millisecond},
		stage{"上游握手", time.Duration(o.ConnectMs) * time.Millisecond},
		stage{"上游首个产出", time.Duration(o.ProbeMs) * time.Millisecond},
	)
	if o.Attempts > 1 {
		t.stages = append(t.stages, stage{fmt.Sprintf("(重试%d次)", o.Attempts-1), 0})
	}
	t.last = time.Now()
}

// done 打印整条时间线，并标出耗时最长的一段。
func (t *timeline) done(extra string) {
	if t == nil {
		return
	}
	t.mark("收尾")

	var b strings.Builder
	worst, worstIdx := time.Duration(0), -1
	for i, s := range t.stages {
		if s.d > worst {
			worst, worstIdx = s.d, i
		}
	}
	for i, s := range t.stages {
		if i > 0 {
			b.WriteString(" | ")
		}
		if i == worstIdx && len(t.stages) > 1 {
			b.WriteString("★")
		}
		b.WriteString(s.name)
		b.WriteString(" ")
		b.WriteString(fmtDur(s.d))
	}
	log.Printf("[timing] %s 总 %s :: %s%s", t.label, fmtDur(time.Since(t.start)), b.String(), extra)
}

func fmtDur(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(100 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(time.Millisecond).String()
	default:
		return "<1ms"
	}
}
