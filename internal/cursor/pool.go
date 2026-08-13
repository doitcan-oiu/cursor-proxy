package cursor

import (
	"math/rand"
	"regexp"
	"sort"
	"sync"
	"time"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/store"
)

// Outcome 一次请求对账号健康的结论。
type Outcome string

const (
	// OutcomeSuccess 成功。
	OutcomeSuccess Outcome = "success"
	// OutcomeRateLimited 限流/额度。
	OutcomeRateLimited Outcome = "rate_limited"
	// OutcomeAuthFailed 鉴权失败/疑似封禁。
	OutcomeAuthFailed Outcome = "auth_failed"
	// OutcomeError 其它错误。
	OutcomeError Outcome = "error"
)

type health struct {
	inFlight      int
	lastStartAt   int64
	cooldownUntil int64
	disabledUntil int64
	// consecutiveFailures 连续失败次数。默认只用来排序（失败多的排后面），
	// 不再让账号变成不可用——上游抖动被算成账号故障，攒够几次就把好账号
	// 隔离半小时，池子小的时候等于自断手脚。
	consecutiveFailures int
	// lastFailureAt 最近一次失败的时间，用于让降权随时间自然消退。
	lastFailureAt int64
	lastOutcome   Outcome
	lastError     string
}

// failurePenaltyMs 是失败降权的有效期。
// 过了这段时间就当没失败过，避免一次偶发抖动永久压着某个账号。
const failurePenaltyMs = 5 * 60 * 1000

// penalty 给出排序用的降权分：数值越大越靠后。
func (h *health) penalty(now int64) int {
	if h.consecutiveFailures == 0 || now-h.lastFailureAt > failurePenaltyMs {
		return 0
	}
	return h.consecutiveFailures
}

var (
	hmu      sync.Mutex
	healthMp = map[string]*health{}
)

func getHealth(id string) *health {
	h := healthMp[id]
	if h == nil {
		h = &health{}
		healthMp[id] = h
	}
	return h
}

// Classification 分类结论。
type Classification struct {
	Outcome   Outcome
	Retryable bool
}

// ClassifyStatus 把上游 HTTP 状态映射成健康结论与是否值得换号重试。
func ClassifyStatus(status int) Classification {
	switch {
	case status == 401 || status == 403:
		return Classification{OutcomeAuthFailed, true}
	case status == 429:
		return Classification{OutcomeRateLimited, true}
	case status == 0 || status >= 500:
		return Classification{OutcomeError, true}
	default:
		return Classification{OutcomeSuccess, false}
	}
}

var (
	reModelRegion = regexp.MustCompile(`(?i)(unsupported_region|not supported in your region|model not available|not_available|change_model)`)
	reModelGone   = regexp.MustCompile(`(?i)(no longer available|no longer supported|is retired|bad_model_name|model name is not valid|model not found|not_found)`)
	rePayment     = regexp.MustCompile(`(?i)(update required|"payment"|payment_required|action_required.*payment|actionrequired.*payment)`)
	reAuth        = regexp.MustCompile(`(?i)(not_logged_in|unauthenticated|unauthorized|invalid.*token|token.*expired)`)
	reBan         = regexp.MustCompile(`(?i)(suspicious|abuse|banned|suspend|blocked|violat)`)
	reRateLimit   = regexp.MustCompile(`(?i)(rate.?limit|too many requests|quota|usage.*limit|free.*limit|trial|resource_exhausted)`)
)

// ClassifyErrorMessage 据流内错误文案判断账号健康与是否换号重试。
func ClassifyErrorMessage(message string) Classification {
	switch {
	case reModelRegion.MatchString(message):
		return Classification{OutcomeSuccess, false}
	case reModelGone.MatchString(message):
		return Classification{OutcomeSuccess, false}
	case rePayment.MatchString(message):
		return Classification{OutcomeSuccess, false}
	case reAuth.MatchString(message):
		return Classification{OutcomeAuthFailed, true}
	case reBan.MatchString(message):
		return Classification{OutcomeAuthFailed, true}
	case reRateLimit.MatchString(message):
		return Classification{OutcomeRateLimited, true}
	default:
		return Classification{OutcomeError, true}
	}
}

// AcquiredAccount 预占到的账号（含标签）。
type AcquiredAccount struct {
	Context
	Label string
}

func nowMs() int64 { return time.Now().UnixMilli() }

// AcquireAccount 选一个健康、未达并发上限、满足最小间隔的账号并预占。
func AcquireAccount(exclude map[string]bool) *AcquiredAccount {
	ab := config.Get().Antiban
	deadline := nowMs() + int64(ab.AcquireTimeoutMs)

	for {
		tokens := store.Read().CursorTokens
		var chosen *store.CursorTokenEntry
		var waitInterval int64 = -1

		hmu.Lock()
		type cand struct {
			t *store.CursorTokenEntry
			h *health
		}
		now := nowMs()
		var usable []cand
		count := 0
		for i := range tokens {
			t := &tokens[i]
			if exclude[t.ID] {
				continue
			}
			count++
			h := getHealth(t.ID)
			if now >= h.cooldownUntil && now >= h.disabledUntil && h.inFlight < ab.MaxConcurrency {
				usable = append(usable, cand{t, h})
			}
		}
		if count == 0 {
			hmu.Unlock()
			return nil
		}
		if len(usable) > 0 {
			// 先按最近是否失败过分层：失败过的排到后面但仍然可用。
			// 这是隔离的替代方案——坏账号不会拖慢每个请求，好账号也不会被误伤下线。
			sort.Slice(usable, func(a, b int) bool {
				pa, pb := usable[a].h.penalty(now), usable[b].h.penalty(now)
				if pa != pb {
					return pa < pb
				}
				if usable[a].h.inFlight != usable[b].h.inFlight {
					return usable[a].h.inFlight < usable[b].h.inFlight
				}
				return usable[a].h.lastStartAt < usable[b].h.lastStartAt
			})
			c := usable[0]
			wi := c.h.lastStartAt + int64(ab.MinIntervalMs) - now
			if wi <= 0 || now+wi > deadline {
				c.h.inFlight++
				c.h.lastStartAt = nowMs()
				tCopy := *c.t
				chosen = &tCopy
			} else {
				waitInterval = wi
			}
		}
		hmu.Unlock()

		if chosen != nil {
			if ab.JitterMs > 0 {
				time.Sleep(time.Duration(rand.Intn(ab.JitterMs)) * time.Millisecond)
			}
			acc := finalizeAcquire(*chosen)
			return &acc
		}

		if waitInterval > 0 {
			time.Sleep(time.Duration(waitInterval) * time.Millisecond)
			continue
		}
		if nowMs() >= deadline {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func finalizeAcquire(entry store.CursorTokenEntry) AcquiredAccount {
	freshBearer := EnsureFreshToken(entry)
	return AcquiredAccount{
		Context: BuildContext(entry.ID, freshBearer, true),
		Label:   entry.Label,
	}
}

// ReleaseAccount 归还账号并更新健康状态。
func ReleaseAccount(id string, outcome Outcome, errorMsg string) {
	if id == "" {
		return
	}
	ab := config.Get().Antiban
	hmu.Lock()
	defer hmu.Unlock()
	h := getHealth(id)
	if h.inFlight > 0 {
		h.inFlight--
	}
	h.lastOutcome = outcome
	h.lastError = errorMsg
	now := nowMs()

	// 失败默认不再让账号下线，只记账用于排序。
	// 冷却与隔离都要显式配了时长才生效（ACCOUNT_COOLDOWN_429_MS / ACCOUNT_QUARANTINE_MS），
	// 默认 0 表示关闭：上游抖动、区域限制、模型不可用这些都会被算成账号故障，
	// 按老逻辑攒够几次就把一个其实好用的账号隔离半小时，池子小的时候直接没号可用。
	switch outcome {
	case OutcomeSuccess:
		h.consecutiveFailures = 0
		h.lastFailureAt = 0
		h.cooldownUntil = 0
		h.disabledUntil = 0
	case OutcomeRateLimited:
		h.consecutiveFailures++
		h.lastFailureAt = now
		if ab.Cooldown429Ms > 0 {
			h.cooldownUntil = now + int64(ab.Cooldown429Ms)
		}
	case OutcomeAuthFailed:
		h.consecutiveFailures++
		h.lastFailureAt = now
		if ab.QuarantineMs > 0 {
			h.disabledUntil = now + int64(ab.QuarantineMs)
		}
	case OutcomeError:
		h.consecutiveFailures++
		h.lastFailureAt = now
		if ab.QuarantineMs > 0 && h.consecutiveFailures >= ab.MaxConsecutiveFailures {
			h.disabledUntil = now + int64(ab.QuarantineMs)
		}
	}
}

// AccountHealthView 供管理接口查看的健康视图。
type AccountHealthView struct {
	ID                  string  `json:"id"`
	Label               string  `json:"label"`
	InFlight            int     `json:"inFlight"`
	Available           bool    `json:"available"`
	CooldownForMs       int64   `json:"cooldownForMs"`
	QuarantinedForMs    int64   `json:"quarantinedForMs"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	LastOutcome         Outcome `json:"lastOutcome,omitempty"`
	LastError           string  `json:"lastError,omitempty"`
}

// HealthSnapshot 返回各账号健康度快照。
func HealthSnapshot() []AccountHealthView {
	ab := config.Get().Antiban
	tokens := store.Read().CursorTokens
	now := nowMs()
	hmu.Lock()
	defer hmu.Unlock()
	out := make([]AccountHealthView, 0, len(tokens))
	for _, t := range tokens {
		h := getHealth(t.ID)
		out = append(out, AccountHealthView{
			ID:                  t.ID,
			Label:               t.Label,
			InFlight:            h.inFlight,
			Available:           now >= h.cooldownUntil && now >= h.disabledUntil && h.inFlight < ab.MaxConcurrency,
			CooldownForMs:       maxInt64(0, h.cooldownUntil-now),
			QuarantinedForMs:    maxInt64(0, h.disabledUntil-now),
			ConsecutiveFailures: h.consecutiveFailures,
			LastOutcome:         h.lastOutcome,
			LastError:           h.lastError,
		})
	}
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
