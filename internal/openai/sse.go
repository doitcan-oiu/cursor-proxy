package openai

import (
	"context"
	"io"
	"net/http"
	"time"

	"cursor-proxy/internal/cursor"
)

const (
	clientClosedStatus = 499
	streamStartGrace   = 2 * time.Second
	streamKeepalive    = 2 * time.Second
)

func clientDisconnected() *cursor.UpstreamError {
	return cursor.NewUpstreamError(clientClosedStatus, "下游已断开连接")
}

type sseResponse struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	ctx        context.Context
	cancel     context.CancelFunc
	started    bool
	failed     bool
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
	if err != nil || n != len(frame) || s.controller.Flush() != nil {
		s.failed = true
		s.cancel()
		return false
	}
	s.lastWrite = time.Now()
	return true
}

func (s *sseResponse) disconnected() bool {
	return s.failed || s.ctx.Err() != nil
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
func (s *sseResponse) awaitOpen(start func(), open func() (*OpenedStream, *cursor.UpstreamError)) (*OpenedStream, *cursor.UpstreamError) {
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
	timer := time.NewTimer(streamStartGrace)
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
