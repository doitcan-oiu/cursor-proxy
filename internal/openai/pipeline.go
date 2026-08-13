package openai

import (
	"math/rand"
	"regexp"
	"time"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/types"
)

var (
	reRegion   = regexp.MustCompile(`(?i)unsupported_region|not supported in your region`)
	reExhaust  = regexp.MustCompile(`(?i)resource_exhausted|actionrequired.*payment|"payment"|rate_limit|update required|no longer supported`)
	reNotFound = regexp.MustCompile(`(?i)not_found`)
)

// FriendlyUpstream 把 Cursor 混淆的原始错误翻译成清晰可读的原因。
func FriendlyUpstream(raw string) string {
	switch {
	case reRegion.MatchString(raw):
		return "该账号所在区域禁用了此模型（第三方模型商区域限制）。请开启内置机场/VPN 或用 CURSOR_HTTP_PROXY 走支持区，或改用 Cursor 自研模型。"
	case reExhaust.MatchString(raw):
		return "该账号额度已用尽（Cursor 返回 resource_exhausted / 需付费）。请更换仍有额度的账号，或开启用量计费。"
	case reNotFound.MatchString(raw):
		return "模型名无效（Cursor 不认识该 model）。请用 /v1/models 返回的模型名。"
	default:
		return "Cursor 上游错误: " + raw
	}
}

// OpenedStream 已锁定账号、缓存首批帧的对话流。
type OpenedStream struct {
	Account  cursor.AcquiredAccount
	Buffered []cursor.StreamEvent
	Stream   *cursor.AgentStream
	// 下面三段耗时用于定位「慢在哪」，只在开了 PROXY_TIMING 时会被打印。
	// AcquireMs 含刷新 token；ConnectMs 是发出请求到拿到响应头；
	// ProbeMs 是拿到响应头到确认这一轮有产出（等于上游的思考时间）。
	AcquireMs int64
	ConnectMs int64
	ProbeMs   int64
	Attempts  int
}

func backoff(attempt int) {
	d := time.Duration(300*(attempt+1))*time.Millisecond + time.Duration(rand.Intn(300))*time.Millisecond
	time.Sleep(d)
}

// ModeFor 按客户端是否声明工具挑选上游模式。
//
// 默认一律 agent 模式。ASK 模式（ASK_MODE=on 才启用）虽然能让纯对话少走一层
// 「写文件再还原」，但它是 Cursor 里「就代码库提问」的模式，上游会限定模型
// 只回答代码相关问题，通用场景下反而会被模型当成拒绝理由。
//
// 声明了工具的客户端任何时候都必须留在 agent 模式，否则内置工具桥接就没得桥了。
func ModeFor(toolCount int) proto.Mode {
	if toolCount > 0 || !config.Get().AskMode {
		return proto.ModeAgent
	}
	return proto.ModeAsk
}

// OpenWithFailover 带故障转移地打开对话流：先探帧，拿到正文前出错则换号重试。
func OpenWithFailover(messages []types.Message, model string, mode proto.Mode) (*OpenedStream, *cursor.UpstreamError) {
	ab := config.Get().Antiban
	exclude := map[string]bool{}
	var lastErr *cursor.UpstreamError
	upstreamModel := cursor.ResolveUpstreamModel(model)

	for attempt := 0; attempt < ab.MaxAttempts; attempt++ {
		t0 := time.Now()
		account := cursor.AcquireAccount(exclude)
		if account == nil {
			break
		}
		acquireMs := time.Since(t0).Milliseconds()

		t1 := time.Now()
		stream, err := cursor.StreamAgent(messages, upstreamModel, account.Bearer, account.ProxyURL, mode)
		connectMs := time.Since(t1).Milliseconds()
		probeStart := time.Now()
		if err != nil {
			status := 0
			if aerr, ok := err.(*cursor.AgentUpstreamError); ok {
				status = aerr.Status
			}
			cls := cursor.ClassifyStatus(status)
			cursor.ReleaseAccount(account.ID, cls.Outcome, err.Error())
			lastErr = cursor.NewUpstreamError(status, err.Error())
			if cls.Retryable {
				if account.ID != "" {
					exclude[account.ID] = true
				}
				backoff(attempt)
				continue
			}
			return nil, lastErr
		}

		var buffered []cursor.StreamEvent
		errEv := ""
		committed := false
		for ev := range stream.Events {
			if ev.Kind == cursor.EventError {
				errEv = ev.Message
				break
			}
			buffered = append(buffered, ev)
			// 正文或工具调用都算「这一轮成功了」。工具调用轮次经常没有正文，
			// 漏掉它会被误判成空响应而重试，白白丢掉调用。
			// 工具参数片段同样算数：上游写长文件时会先发几十个片段、最后才发完整调用，
			// 漏掉它会把这些片段全压在缓冲里，等到调用结束才一次性放行，流式就没了。
			// 思考内容不算——上游存在「只吐思考、不给正文」的空转轮次。
			if ev.Kind == cursor.EventToolCall ||
				(ev.Kind == cursor.EventToolInputDelta && ev.Text != "") ||
				(ev.Kind == cursor.EventDelta && ev.Text != "") {
				committed = true
				break
			}
			if ev.Kind == cursor.EventEnd {
				break
			}
		}

		// 上游偶尔会开流即结束、只吐思考不给正文。这种空转对 agent 客户端是致命的
		// （它会以为轮次结束而卡住），重试一次通常就正常了。
		//
		// 注意这里既不排除账号、也不记为账号失败：空响应是上游抖动，不是账号的问题。
		// 早期版本按失败处理，结果单账号场景下立刻「无可用账号」，连续几次还会把
		// 唯一的账号隔离掉。
		if errEv == "" && !committed {
			stream.Close()
			cursor.ReleaseAccount(account.ID, cursor.OutcomeSuccess, "")
			lastErr = cursor.NewUpstreamError(502, "upstream returned an empty response")
			backoff(attempt)
			continue
		}

		if errEv != "" && !committed {
			cls := cursor.ClassifyErrorMessage(errEv)
			cursor.ReleaseAccount(account.ID, cls.Outcome, errEv)
			stream.Close()
			status := 502
			switch cls.Outcome {
			case cursor.OutcomeRateLimited:
				status = 429
			case cursor.OutcomeAuthFailed:
				status = 401
			case cursor.OutcomeSuccess:
				status = 400
			}
			lastErr = cursor.NewUpstreamError(status, errEv)
			if cls.Retryable {
				if account.ID != "" {
					exclude[account.ID] = true
				}
				backoff(attempt)
				continue
			}
			return nil, lastErr
		}

		return &OpenedStream{
			Account: *account, Buffered: buffered, Stream: stream,
			AcquireMs: acquireMs, ConnectMs: connectMs,
			ProbeMs: time.Since(probeStart).Milliseconds(), Attempts: attempt + 1,
		}, nil
	}

	if lastErr != nil {
		return nil, cursor.NewUpstreamError(503, "all accounts failed: "+lastErr.Msg)
	}
	return nil, cursor.NewUpstreamError(503, "no available account")
}

// Chain 先吐已缓存帧，再继续消费剩余流，统一到一个 channel。
func Chain(buffered []cursor.StreamEvent, stream *cursor.AgentStream) <-chan cursor.StreamEvent {
	out := make(chan cursor.StreamEvent, 32)
	go func() {
		defer close(out)
		for _, ev := range buffered {
			out <- ev
		}
		for ev := range stream.Events {
			out <- ev
		}
	}()
	return out
}
