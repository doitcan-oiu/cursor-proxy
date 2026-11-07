package openai

import "cursor-proxy/internal/store"

type inputUsage struct {
	total      int
	cacheRead  int
	cacheWrite int
	simulated  bool
}

// splitInputUsage 按请求开始时的设置划分输入 token，整条 SSE 流复用同一份用量。
func splitInputUsage(total int, settings store.SimulatedCacheSettings) inputUsage {
	return inputUsage{
		total:      total,
		cacheRead:  int(int64(total) * int64(settings.ReadPercent) / 100),
		cacheWrite: int(int64(total) * int64(settings.WritePercent) / 100),
		simulated:  settings.ReadPercent > 0 || settings.WritePercent > 0,
	}
}

func (input inputUsage) openAI(completionTokens int) map[string]any {
	usage := map[string]any{
		"prompt_tokens":     input.total,
		"completion_tokens": completionTokens,
		"total_tokens":      input.total + completionTokens,
	}
	if input.simulated {
		usage["prompt_tokens_details"] = map[string]int{
			"cached_tokens":      input.cacheRead,
			"cache_write_tokens": input.cacheWrite,
		}
	}
	return usage
}

func (input inputUsage) anthropic(outputTokens int) map[string]any {
	usage := map[string]any{
		"input_tokens":  input.total - input.cacheRead - input.cacheWrite,
		"output_tokens": outputTokens,
	}
	if input.simulated {
		usage["cache_read_input_tokens"] = input.cacheRead
		usage["cache_creation_input_tokens"] = input.cacheWrite
	}
	return usage
}
