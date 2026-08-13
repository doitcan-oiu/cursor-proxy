package cursor

import (
	"testing"

	"cursor-proxy/internal/config"
)

// resetHealth 清掉全局健康表，让每个用例从干净状态开始。
func resetHealth(t *testing.T) {
	t.Helper()
	hmu.Lock()
	healthMp = map[string]*health{}
	hmu.Unlock()
}

// 「失败」这个信号并不可靠：上游抖动、区域限制、模型名无效都会算成账号故障。
// 按老逻辑攒够几次就把一个其实好用的账号隔离半小时，池子小的时候直接没号可用。
func TestErrorsDoNotDisableAccountByDefault(t *testing.T) {
	t.Setenv("ACCOUNT_QUARANTINE_MS", "")
	t.Setenv("ACCOUNT_COOLDOWN_429_MS", "")
	config.Reset()
	resetHealth(t)

	for i := 0; i < 10; i++ {
		ReleaseAccount("acc-1", OutcomeError, "上游抖动")
	}

	hmu.Lock()
	h := getHealth("acc-1")
	disabled, cooling := h.disabledUntil, h.cooldownUntil
	failures := h.consecutiveFailures
	hmu.Unlock()

	if disabled != 0 {
		t.Fatalf("默认不应隔离账号，disabledUntil=%d", disabled)
	}
	if cooling != 0 {
		t.Fatalf("默认不应冷却账号，cooldownUntil=%d", cooling)
	}
	if failures != 10 {
		t.Fatalf("失败次数仍要记账用于排序，实际 %d", failures)
	}
}

// 限流与鉴权失败同样不下线，只降权。
func TestRateLimitAndAuthFailDoNotDisableByDefault(t *testing.T) {
	t.Setenv("ACCOUNT_QUARANTINE_MS", "")
	t.Setenv("ACCOUNT_COOLDOWN_429_MS", "")
	config.Reset()
	resetHealth(t)

	ReleaseAccount("acc-1", OutcomeRateLimited, "429")
	ReleaseAccount("acc-2", OutcomeAuthFailed, "401")

	hmu.Lock()
	defer hmu.Unlock()
	if h := getHealth("acc-1"); h.cooldownUntil != 0 {
		t.Fatalf("限流默认不应冷却，cooldownUntil=%d", h.cooldownUntil)
	}
	if h := getHealth("acc-2"); h.disabledUntil != 0 {
		t.Fatalf("鉴权失败默认不应隔离，disabledUntil=%d", h.disabledUntil)
	}
}

// 配了时长就还是按老行为走，方便账号很多、希望坏号彻底让路的场景。
func TestQuarantineStillWorksWhenConfigured(t *testing.T) {
	t.Setenv("ACCOUNT_QUARANTINE_MS", "60000")
	t.Setenv("ACCOUNT_MAX_FAILURES", "2")
	config.Reset()
	resetHealth(t)
	defer func() {
		t.Setenv("ACCOUNT_QUARANTINE_MS", "")
		t.Setenv("ACCOUNT_MAX_FAILURES", "")
		config.Reset()
	}()

	ReleaseAccount("acc-1", OutcomeError, "boom")
	hmu.Lock()
	first := getHealth("acc-1").disabledUntil
	hmu.Unlock()
	if first != 0 {
		t.Fatal("没到阈值不应隔离")
	}

	ReleaseAccount("acc-1", OutcomeError, "boom")
	hmu.Lock()
	second := getHealth("acc-1").disabledUntil
	hmu.Unlock()
	if second == 0 {
		t.Fatal("达到阈值且配了时长时应隔离")
	}
}

// 成功一次立刻恢复：失败计数清零，降权消失。
func TestSuccessClearsPenalty(t *testing.T) {
	config.Reset()
	resetHealth(t)

	ReleaseAccount("acc-1", OutcomeError, "boom")
	ReleaseAccount("acc-1", OutcomeError, "boom")

	hmu.Lock()
	now := nowMs()
	before := getHealth("acc-1").penalty(now)
	hmu.Unlock()
	if before == 0 {
		t.Fatal("刚失败过应当被降权")
	}

	ReleaseAccount("acc-1", OutcomeSuccess, "")
	hmu.Lock()
	after := getHealth("acc-1").penalty(nowMs())
	hmu.Unlock()
	if after != 0 {
		t.Fatalf("成功一次应立刻恢复优先级，实际降权 %d", after)
	}
}

// 降权只在一段时间内有效，一次偶发抖动不该永久压着某个账号。
func TestPenaltyDecaysOverTime(t *testing.T) {
	h := &health{consecutiveFailures: 3, lastFailureAt: 1000}

	if got := h.penalty(1000 + failurePenaltyMs - 1); got != 3 {
		t.Fatalf("有效期内应保持降权，实际 %d", got)
	}
	if got := h.penalty(1000 + failurePenaltyMs + 1); got != 0 {
		t.Fatalf("过了有效期应当消退，实际 %d", got)
	}
	if got := (&health{}).penalty(nowMs()); got != 0 {
		t.Fatalf("没失败过不应有降权，实际 %d", got)
	}
}
