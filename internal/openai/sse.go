package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"cursor-proxy/internal/cursor"
)

const (
	clientClosedStatus = 499
	streamStartGrace   = 2 * time.Second
	streamKeepalive    = 2 * time.Second
	toolArgsChunkBytes = 8 << 10
)

// writeToolArgs 把已组装好的工具参数拆成可拼接的增量，避免长文件挤进一个 SSE 事件。
func writeToolArgs(arguments string, emit func(first bool, part string) bool) bool {
	for first := true; ; first = false {
		end := min(len(arguments), toolArgsChunkBytes)
		for end > 0 && end < len(arguments) && !utf8.RuneStart(arguments[end]) {
			end--
		}
		if end == 0 && len(arguments) > 0 {
			end = min(len(arguments), toolArgsChunkBytes)
		}
		if !emit(first, arguments[:end]) {
			return false
		}
		if end == len(arguments) {
			return true
		}
		arguments = arguments[end:]
	}
}

func clientDisconnected() *cursor.UpstreamError {
	return cursor.NewUpstreamError(clientClosedStatus, "下游已断开连接")
}

type sseResponse struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	ctx        context.Context
	cancel     context.CancelFunc
	started    bool
	writeErr   error
	lastWrite  time.Time
	firstToken time.Time
}

func newSSEResponse(ctx context.Context, cancel context.CancelFunc, w http.ResponseWriter) *sseResponse {
	return &sseResponse{w: w, controller: http.NewResponseController(w), ctx: ctx, cancel: cancel}
}

// write 在 handler 自己的 goroutine 中写入并刷新一帧。首个写入失败会停止上游。
func (s *sseResponse) write(frame string) bool {
	if s.disconnected() {
		return false
	}
	if !s.started {
		h := s.w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache, no-transform")
		h.Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	n, err := io.WriteString(s.w, frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.writeErr = fmt.Errorf("写入: %w", err)
	} else if err := s.controller.Flush(); err != nil {
		s.writeErr = fmt.Errorf("刷新: %w", err)
	}
	if s.writeErr != nil {
		s.cancel()
		return false
	}
	s.lastWrite = time.Now()
	return true
}

func (s *sseResponse) disconnected() bool {
	return s.writeErr != nil || s.ctx.Err() != nil
}

// disconnectMessage 区分对端取消和代理写出失败，并保留断流时的进度。
func (s *sseResponse) disconnectMessage() string {
	message := clientDisconnected().Msg
	if s == nil {
		return message
	}
	stage := "起始帧发出前"
	if !s.firstToken.IsZero() {
		stage = "已有输出"
	} else if !s.lastWrite.IsZero() {
		stage = "已发起始帧，尚无输出"
	}
	if s.writeErr != nil {
		return fmt.Sprintf("SSE 向下游写出失败（%s）: %v", stage, s.writeErr)
	}
	return message + "（" + stage + "）"
}

func (s *sseResponse) markOutput() {
	if s.firstToken.IsZero() {
		s.firstToken = s.lastWrite
	}
}

func (s *sseResponse) firstTokenMs(startedAt time.Time) *int64 {
	if s.firstToken.IsZero() {
		return nil
	}
	ms := s.firstToken.Sub(startedAt).Milliseconds()
	return &ms
}

func (s *sseResponse) keepalive() {
	if time.Since(s.lastWrite) >= streamKeepalive {
		s.write(": keepalive\n\n")
	}
}

// awaitOpen 保留快速失败的 HTTP 状态。上游等待较久时先发协议起始帧，
// 让中转服务的首事件超时停止计时，之后仍可以在流中报告错误。
func (s *sseResponse) awaitOpen(startedAt time.Time, start func(), open func() (*OpenedStream, *cursor.UpstreamError)) (*OpenedStream, *cursor.UpstreamError) {
	type result struct {
		stream *OpenedStream
		err    *cursor.UpstreamError
	}
	ready := make(chan result)
	go func() {
		stream, err := open()
		select {
		case ready <- result{stream, err}:
		case <-s.ctx.Done():
			if stream != nil {
				stream.Stream.Close()
				cursor.ReleaseAccount(stream.Account.ID, cursor.OutcomeCanceled, "")
			}
		}
	}()
	// 大请求的读入和分词也占用了下一跳的首包预算。
	timer := time.NewTimer(time.Until(startedAt.Add(streamStartGrace)))
	defer timer.Stop()
	for {
		select {
		case result := <-ready:
			return result.stream, result.err
		case <-s.ctx.Done():
			return nil, clientDisconnected()
		case <-timer.C:
			if s.started {
				s.keepalive()
			} else {
				start()
			}
			timer.Reset(streamKeepalive)
		}
	}
}

func (s *sseResponse) next(events <-chan cursor.StreamEvent, ticks <-chan time.Time) (cursor.StreamEvent, bool) {
	for !s.disconnected() {
		select {
		case ev, ok := <-events:
			return ev, ok && !s.disconnected()
		case <-ticks:
			s.keepalive()
		case <-s.ctx.Done():
		}
	}
	return cursor.StreamEvent{}, false
}
