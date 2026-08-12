package tools

import "strings"

// LiveWriter 让「写文件」类调用的内容能边收边吐。
//
// 上游是 agent 形态：纯对话里问它「写一段 SVG」「写个脚本」，它会把内容流式塞进
// 一次写文件调用。若等整个调用发完再还原，长内容会先静默几十秒再整段出现。
// 这里在收到内容时就逐段透传，只在开头补一次代码块围栏。
//
// 只在客户端没声明工具时启用；声明了工具的场景应当把调用原样转成 tool_calls。
type LiveWriter struct {
	enabled bool
	// activeID 是当前正在流式输出的调用；空表示没有进行中的输出。
	activeID string
	// pending 是还没决定用什么围栏时暂存的内容。
	// 上游的「进行中」帧有时先到、路径却还是空的，等一小段内容再判断更准。
	pending string
	// decided 表示围栏已经确定，后续片段可以直接透传。
	decided bool
	// chunks 是定下围栏前收到的片段数。
	chunks int
	// close 是当前代码块的收尾围栏；散文类内容为空。
	close string
	// wrote 表示已经吐出过内容，lastChar 用于决定收尾要不要补换行。
	wrote    bool
	lastChar byte
}

// sniffAfter 是路径未知时，先攒够多少字节再靠内容猜类型。
// 攒得太少猜不准，太多会让开头卡顿，几十字节足够看出是不是标记语言。
const sniffAfter = 64

// NewLiveWriter 构造一个流式写出器。enabled 为假时所有方法都返回空串。
func NewLiveWriter(enabled bool) *LiveWriter {
	return &LiveWriter{enabled: enabled}
}

// streamableKind 判断某类调用的参数内容值不值得直接当正文吐出来。
// 只有写文件是「内容即答案」，其余工具的参数是路径或命令，吐出来只会是噪音。
func streamableKind(kind NativeKind) bool {
	return kind == KindWriteFile
}

// Push 处理一段参数片段，返回此刻应当输出的文本。
func (w *LiveWriter) Push(n *Native, text string) string {
	if !w.enabled || n == nil || text == "" || !streamableKind(n.Kind) {
		return ""
	}

	var out strings.Builder
	if w.activeID != n.ID {
		out.WriteString(w.closeActive())
		w.activeID = n.ID
	}

	if !w.decided {
		w.pending += text
		w.chunks++
		// 路径一到就能准确判断。上游的路径帧总是紧跟在第一个内容片段之后，
		// 所以多攒一个片段就能等到它；等不到再靠内容猜，别让开头一直卡着。
		if n.Path == "" && w.chunks < 2 && len(w.pending) < sniffAfter {
			return out.String()
		}
		out.WriteString(w.decide(n.Path))
		return out.String()
	}

	out.WriteString(text)
	w.mark(text)
	return out.String()
}

// decide 定下围栏并把暂存内容一次吐出。
func (w *LiveWriter) decide(path string) string {
	w.decided = true
	body := w.pending
	w.pending = ""

	var open string
	if path != "" {
		open, w.close = fenceFor(path, "")
	} else {
		open, w.close = sniffFence(body)
	}
	w.mark(body)
	return open + body
}

func (w *LiveWriter) mark(text string) {
	if text == "" {
		return
	}
	w.wrote = true
	w.lastChar = text[len(text)-1]
}

// Finish 在调用完成时收尾。handled 为真表示这次调用的内容已经流式给过，
// 调用方应跳过后续的还原逻辑，否则同一份内容会被输出两遍。
func (w *LiveWriter) Finish(n *Native) (text string, handled bool) {
	if !w.enabled || n == nil || w.activeID == "" || w.activeID != n.ID {
		return "", false
	}
	return w.closeActive(), true
}

// Interrupt 在有正文插入或流结束时收尾未闭合的代码块，
// 避免输出里留下半个围栏。
func (w *LiveWriter) Interrupt() string {
	if !w.enabled {
		return ""
	}
	return w.closeActive()
}

func (w *LiveWriter) closeActive() string {
	if w.activeID == "" {
		return ""
	}
	var out string
	// 内容太短、还没定下围栏就结束了，此刻按已有内容决定
	if !w.decided && w.pending != "" {
		out = w.decide("")
	}

	wrote, close := w.wrote, w.close
	w.activeID, w.decided, w.pending, w.chunks = "", false, "", 0
	w.wrote, w.close = false, ""

	switch {
	case close == "":
		// 散文类内容没有围栏，补个换行与后续正文隔开
		if wrote && w.lastChar != '\n' {
			return out + "\n"
		}
		return out
	case wrote && w.lastChar != '\n':
		return out + "\n" + close + "\n"
	default:
		return out + close + "\n"
	}
}

// sniffFence 在拿不到文件路径时，靠内容开头猜该用什么围栏。
//
// 猜不出就退化成无语言标记的代码块：写文件的内容绝大多数是代码或标记语言，
// 套上围栏最多是少个高亮，不套却会让 XML、JSON 这类内容被客户端当标签吞掉。
func sniffFence(body string) (open, close string) {
	s := strings.TrimLeft(body, " \t\r\n")
	lang := ""
	switch {
	case strings.HasPrefix(s, "<!DOCTYPE html"), strings.HasPrefix(s, "<html"):
		lang = "html"
	case strings.HasPrefix(s, "<?xml"), strings.HasPrefix(s, "<svg"), strings.HasPrefix(s, "<"):
		lang = "xml"
	case strings.HasPrefix(s, "{"), strings.HasPrefix(s, "["):
		lang = "json"
	case strings.HasPrefix(s, "#!"):
		lang = "bash"
	}
	ticks := strings.Repeat("`", maxTickRun(body)+1)
	return ticks + lang + "\n", ticks
}
