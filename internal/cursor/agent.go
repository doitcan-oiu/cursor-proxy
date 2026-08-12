package cursor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/types"
)

const agentBase = "https://agentn.api5.cursor.sh"

func runURL() string         { return agentBase + "/agent.v1.AgentService/Run" }
func agentModelsURL() string { return agentBase + "/agent.v1.AgentService/GetUsableModels" }

// AgentUpstreamError agent 端点的上游错误。
type AgentUpstreamError struct {
	Status int
	Msg    string
}

func (e *AgentUpstreamError) Error() string { return e.Msg }

// NewAgentUpstreamError 构造 agent 上游错误。
func NewAgentUpstreamError(status int, msg string) *AgentUpstreamError {
	return &AgentUpstreamError{Status: status, Msg: msg}
}

func agentHeaders(bearer, contentType string) http.Header {
	requestID := uuidV5(bearer + time.Now().String())[:36]
	h := http.Header{}
	h.Set("authorization", "Bearer "+bearer)
	h.Set("connect-protocol-version", "1")
	h.Set("content-type", contentType)
	h.Set("x-cursor-client-type", "cli")
	h.Set("x-cursor-client-version", config.Get().AgentClientVersion)
	h.Set("x-ghost-mode", "true")
	h.Set("x-request-id", requestID)
	h.Set("x-amzn-trace-id", "Root="+requestID)
	return h
}

func connectFrame(payload []byte) []byte {
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	return append(header, payload...)
}

// AgentModel agent 端点返回的模型。
type AgentModel struct {
	ID          string   `json:"id"`
	Aliases     []string `json:"aliases"`
	DisplayName string   `json:"displayName,omitempty"`
}

// GetUsableModels 拉取当前账号可用模型（agent 端点）。
func GetUsableModels(token, proxyURL string) ([]AgentModel, error) {
	req, _ := http.NewRequest(http.MethodPost, agentModelsURL(), strings.NewReader("{}"))
	req.Header = agentHeaders(auth.ExtractBearer(token), "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := httpx.Client(proxyURL).Do(req)
	if err != nil {
		return nil, NewAgentUpstreamError(0, err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, NewAgentUpstreamError(resp.StatusCode, snippet(raw, 300))
	}
	var data struct {
		Models []struct {
			ModelID     string   `json:"modelId"`
			Aliases     []string `json:"aliases"`
			DisplayName string   `json:"displayName"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil, NewAgentUpstreamError(resp.StatusCode, "bad json")
	}
	out := make([]AgentModel, 0, len(data.Models))
	for _, m := range data.Models {
		if m.ModelID == "" {
			continue
		}
		// aliases 为空时也要序列化成 []，否则前端拿到 null 会在 .includes 上炸
		aliases := m.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		out = append(out, AgentModel{ID: m.ModelID, Aliases: aliases, DisplayName: m.DisplayName})
	}
	return out, nil
}

func parseErrorFrame(utf string) string {
	var j struct {
		Error *struct {
			Code    string `json:"code"`
			Details []struct {
				Debug struct {
					Error   string `json:"error"`
					Details struct {
						Title  string `json:"title"`
						Detail string `json:"detail"`
					} `json:"details"`
				} `json:"debug"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(utf), &j) != nil || j.Error == nil {
		return ""
	}
	if len(j.Error.Details) > 0 {
		d := j.Error.Details[0].Debug
		switch {
		case d.Details.Detail != "":
			return d.Details.Detail
		case d.Details.Title != "":
			return d.Details.Title
		case d.Error != "":
			return d.Error
		}
	}
	if j.Error.Code != "" {
		return j.Error.Code
	}
	return snippet([]byte(utf), 200)
}

// AgentStream 是一个可取消的对话流。
type AgentStream struct {
	Events <-chan StreamEvent
	cancel context.CancelFunc
}

// Close 取消流并释放底层连接。
func (s *AgentStream) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// StreamAgent 向 agentn.api5 发起 Run 流式对话，返回事件流。
// 初始请求失败返回 error；流内错误以 EventError 事件下发。
//
// 收尾优先看 Connect 的 end-of-stream 帧，拿到即立刻结束；
// 空闲超时只作为上游不发该帧时的兜底。
func StreamAgent(messages []types.Message, modelID, token, proxyURL string) (*AgentStream, error) {
	bearer := auth.ExtractBearer(token)
	body := connectFrame(proto.EncodeAgentRequest(messages, modelID))

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, runURL(), strings.NewReader(string(body)))
	req.Header = agentHeaders(bearer, "application/connect+proto")

	resp, err := httpx.Client(proxyURL).Do(req)
	if err != nil {
		cancel()
		return nil, NewAgentUpstreamError(0, err.Error())
	}
	if resp.StatusCode != http.StatusOK || resp.Body == nil {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		msg := snippet(raw, 300)
		if msg == "" {
			msg = resp.Status
		}
		return nil, NewAgentUpstreamError(resp.StatusCode, msg)
	}

	events := make(chan StreamEvent, 32)
	go pumpAgentStream(ctx, resp.Body, events)
	return &AgentStream{Events: events, cancel: cancel}, nil
}

func pumpAgentStream(ctx context.Context, body io.ReadCloser, events chan<- StreamEvent) {
	defer close(events)
	defer body.Close()

	cfg := config.Get()
	idle := time.Duration(cfg.AgentIdleMs) * time.Millisecond
	finishIdle := time.Duration(cfg.AgentFinishIdleMs) * time.Millisecond
	hardCap := time.Duration(cfg.AgentHardCapMs) * time.Millisecond
	firstToken := time.Duration(cfg.AgentFirstTokenMs) * time.Millisecond
	start := time.Now()

	// 独立 goroutine 读取网络字节，主循环 select 实现空闲/超时判定。
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 16*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case chunks <- chunk{data: cp}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case chunks <- chunk{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	var buffer []byte
	var state agentStreamState

	send := func(ev StreamEvent) bool {
		select {
		case events <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	timer := time.NewTimer(idle)
	defer timer.Stop()

	for {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		// 一旦上游回写了会话记录（本轮结束），只再留一个很短的收尾窗口收残余帧，
		// 而不是干等整个 idle。上游永不关连接、只发心跳，这是避免尾部空等的关键。
		wait := idle
		if state.turnComplete {
			wait = finishIdle
		}
		timer.Reset(wait)

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if state.turnComplete || state.gotContent {
				if !state.errored {
					send(StreamEvent{Kind: EventEnd})
				}
				return
			}
			if time.Since(start) > firstToken {
				state.errored = true
				send(StreamEvent{Kind: EventError, Message: fmt.Sprintf(
					"上游 %ds 未返回任何内容：出口节点可能不通或该模型在当前出口区域不可用。请在界面切换 VPN 节点后重试，或改用 auto 等可用模型。",
					cfg.AgentFirstTokenMs/1000)})
				return
			}
			continue
		case ch := <-chunks:
			if ch.err != nil {
				if !state.errored && ch.err != io.EOF && !state.gotContent && !state.turnComplete {
					send(StreamEvent{Kind: EventError, Message: ch.err.Error()})
					state.errored = true
				} else if !state.errored {
					send(StreamEvent{Kind: EventEnd})
				}
				return
			}
			if time.Since(start) > hardCap {
				if !state.errored {
					send(StreamEvent{Kind: EventEnd})
				}
				return
			}
			buffer = append(buffer, ch.data...)
			if endOfStream := state.process(&buffer, send); endOfStream {
				if !state.errored {
					send(StreamEvent{Kind: EventEnd})
				}
				return
			}
		}
	}
}

// 上游帧的顶层字段号（逆向自 agentn.api5 的实际报文）。
const (
	fieldStreamMessage = 1  // 流式增量
	fieldConversation  = 4  // 会话记录回写
	fieldHeartbeat     = 13 // StreamMessage 内的心跳（约 10s 一次）
)

// agentStreamState 跟踪一次流的解析状态。
type agentStreamState struct {
	gotContent bool // 是否已产出过正文
	errored    bool // 是否已下发过错误
	// sawGeneration 表示生成阶段已开始。用于区分开头回写的会话上下文
	// 与结尾回写的会话记录——两者都是 fieldConversation 帧。
	sawGeneration bool
	// turnComplete 表示上游已把整轮对话持久化，即本轮生成结束。
	turnComplete bool
}

// process 消费缓冲里完整的帧，返回是否遇到协议级结束帧。
func (s *agentStreamState) process(buffer *[]byte, send func(StreamEvent) bool) bool {
	b := *buffer
	pos := 0
	for pos+5 <= len(b) {
		flag := b[pos]
		length := binary.BigEndian.Uint32(b[pos+1 : pos+5])
		if pos+5+int(length) > len(b) {
			break
		}
		payload := b[pos+5 : pos+5+int(length)]
		pos += 5 + int(length)
		if frameDebug {
			debugFrame(flag, length, payload)
		}
		if length == 0 {
			continue
		}

		if flag&0x01 != 0 {
			raw, err := gunzipBytes(payload)
			if err != nil {
				continue
			}
			payload = raw
		}
		if flag&0x02 != 0 {
			// Connect 协议的 end-of-stream 帧。agentn.api5 目前不发这个，
			// 但按协议处理，换端点或上游改行为时都能立刻收尾。
			utf := strings.TrimSpace(string(payload))
			if utf != "" {
				if e := parseErrorFrame(utf); e != "" {
					s.errored = true
					send(StreamEvent{Kind: EventError, Message: e})
				}
			}
			*buffer = b[pos:]
			return true
		}

		top := proto.Decode(payload)

		// 心跳帧：上游每 10s 一个，不代表有新数据。
		if sm := proto.FirstBytes(top, fieldStreamMessage); sm != nil {
			smFields := proto.Decode(sm)
			if _, isHeartbeat := smFields[fieldHeartbeat]; isHeartbeat && len(smFields) == 1 {
				continue
			}
			s.sawGeneration = true
		}

		// 会话记录回写。开头也会回写一次上下文，所以只有在生成已经开始之后
		// 出现，才说明这一轮真的结束了。
		if _, ok := top[fieldConversation]; ok {
			if s.sawGeneration {
				s.turnComplete = true
			}
			continue
		}

		utf := string(payload)
		if strings.Contains(utf, `"error"`) {
			if e := parseErrorFrame(utf); e != "" {
				s.errored = true
				send(StreamEvent{Kind: EventError, Message: e})
			}
			continue
		}

		delta := proto.DecodeAgentFrame(payload)
		if delta.Content != "" || delta.Thinking != "" {
			// 记录回写之后又来内容，说明这一轮还没完，撤回结束判定。
			s.turnComplete = false
			if delta.Content != "" {
				s.gotContent = true
			}
			send(StreamEvent{Kind: EventDelta, Text: delta.Content, Thinking: delta.Thinking})
		}
	}
	*buffer = b[pos:]
	return false
}
