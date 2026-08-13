package cursor

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"

	"cursor-proxy/internal/proto"
)

// StreamEventKind 标识流事件类型。
type StreamEventKind int

const (
	// EventDelta 增量内容。
	EventDelta StreamEventKind = iota
	// EventError 流内错误。
	EventError
	// EventEnd 流正常结束。
	EventEnd
	// EventToolCall 上游要求客户端执行一次工具。
	EventToolCall
	// EventToolInputDelta 工具参数的流式片段。
	// 上游把「写文件」这类调用的内容逐段发来，纯对话场景下可据此实现真正的流式输出。
	EventToolInputDelta
)

// NativeToolKind 是上游内置工具的类型。
type NativeToolKind string

const (
	// ToolReadFile 读取文件，参数为路径。
	ToolReadFile NativeToolKind = "read_file"
	// ToolRunTerminal 执行终端命令，参数为命令行。
	ToolRunTerminal NativeToolKind = "run_terminal"
	// ToolSearchFiles 按 glob 模式搜索文件。
	ToolSearchFiles NativeToolKind = "search_files"
	// ToolWriteFile 写入文件。
	ToolWriteFile NativeToolKind = "write_file"
	// ToolTask 派发一个子 agent 去完成子任务。
	ToolTask NativeToolKind = "task"
	// ToolDeleteFile 删除文件。
	ToolDeleteFile NativeToolKind = "delete_file"
	// ToolListFiles 按 glob 列出文件。
	ToolListFiles NativeToolKind = "list_files"
	// ToolFetchURL 抓取网页。
	ToolFetchURL NativeToolKind = "fetch_url"
	// ToolTodoWrite 记录待办清单。
	ToolTodoWrite NativeToolKind = "todo_write"
	// ToolUnknown 是尚未识别的上游工具。保留它是为了不让对话静默中断——
	// 未映射的工具会以说明文本告知客户端，并在日志里打出字段号便于补齐。
	ToolUnknown NativeToolKind = "unknown"
)

// NativeToolCall 是上游发来的一次内置工具调用请求。
//
// Cursor 的 agent 并不自己执行工具，而是把调用下发给客户端执行。
// 早期实现忽略了这些帧，表现为「模型说要做某事，然后对话就断了」。
type NativeToolCall struct {
	ID          string
	Kind        NativeToolKind
	Path        string // ToolReadFile / ToolWriteFile
	Command     string // ToolRunTerminal
	Pattern     string // ToolSearchFiles
	Content     string // ToolWriteFile
	Prompt      string // ToolTask
	URL         string // ToolFetchURL
	Description string
	// Field 是未识别工具在参数容器里的字段号，用于日志排查与后续补齐。
	Field int
	// Raw 是未识别工具的原始参数字节，留档供补映射时复核。
	Raw []byte
}

// StreamEvent 是对话流里产出的单个事件。
type StreamEvent struct {
	Kind     StreamEventKind
	Text     string
	Thinking string
	Message  string
	Tool     *NativeToolCall
	// Truncated 只在 EventEnd 上有意义：本轮不是模型自己说完的，
	// 而是被代理的时长上限掐断的。客户端据此把结束原因报成「长度不足」，
	// 而不是让半截回答看起来像正常收尾。
	Truncated bool
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func gunzipBytes(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// EncodeConnectEnvelope 把 protobuf 主体封装为 Connect 信封：[flag:1][len:4 BE][payload]。
func EncodeConnectEnvelope(body []byte, compress bool) []byte {
	payload := body
	flag := byte(0x00)
	if compress {
		payload = gzipBytes(body)
		flag = 0x01
	}
	header := make([]byte, 5)
	header[0] = flag
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	return append(header, payload...)
}

// FrameDecoder 是有状态的 Connect 帧解码器，跨 chunk 缓冲，逐帧产出事件。
// flag 0/1: protobuf(可 gzip)；flag 2/3: JSON(可 gzip，一般是状态/错误)。
type FrameDecoder struct {
	buffer []byte
}

// Push 喂入一段网络字节，返回本次可解出的事件。
func (d *FrameDecoder) Push(chunk []byte) []StreamEvent {
	d.buffer = append(d.buffer, chunk...)
	var events []StreamEvent

	for len(d.buffer) >= 5 {
		flag := d.buffer[0]
		length := binary.BigEndian.Uint32(d.buffer[1:5])
		if uint32(len(d.buffer)) < 5+length {
			break
		}
		payload := d.buffer[5 : 5+length]
		d.buffer = d.buffer[5+length:]
		if length == 0 {
			continue
		}

		switch {
		case flag == 0x00 || flag == 0x01:
			raw := payload
			if flag == 0x01 {
				var err error
				if raw, err = gunzipBytes(payload); err != nil {
					events = append(events, StreamEvent{Kind: EventError, Message: "frame decode failed: " + err.Error()})
					continue
				}
			}
			dec := proto.DecodeChatResponse(raw)
			if dec.Text != "" || dec.Thinking != "" {
				events = append(events, StreamEvent{Kind: EventDelta, Text: dec.Text, Thinking: dec.Thinking})
			}
		case flag == 0x02 || flag == 0x03:
			raw := payload
			if flag == 0x03 {
				var err error
				if raw, err = gunzipBytes(payload); err != nil {
					continue
				}
			}
			utf := strings.TrimSpace(string(raw))
			if utf != "" && utf != "{}" {
				var probe map[string]json.RawMessage
				if json.Unmarshal([]byte(utf), &probe) == nil {
					if _, ok := probe["error"]; ok {
						events = append(events, StreamEvent{Kind: EventError, Message: utf})
					}
				}
			}
		}
	}
	return events
}
