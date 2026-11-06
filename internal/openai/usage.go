package openai

import "cursor-proxy/internal/config"

// simulatedCacheTokens 只划分 usage 中估算的输入 token。
func simulatedCacheTokens(inputTokens int) (cached int, enabled bool) {
	percent := config.Get().SimulatedCachePercent
	return int(int64(inputTokens) * int64(percent) / 100), percent > 0
}

func usagePayload(promptTokens, completionTokens int) map[string]any {
	usage := map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
	if cached, enabled := simulatedCacheTokens(promptTokens); enabled {
		usage["prompt_tokens_details"] = map[string]int{"cached_tokens": cached}
	}
	return usage
}

func anthropicUsage(inputTokens, outputTokens int) map[string]any {
	usage := map[string]any{
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
	}
	if cached, enabled := simulatedCacheTokens(inputTokens); enabled {
		// Anthropic 的 input_tokens 只包含未命中的部分。
		usage["input_tokens"] = inputTokens - cached
		usage["cache_read_input_tokens"] = cached
		usage["cache_creation_input_tokens"] = 0
	}
	return usage
}
