package openai

import (
	"reflect"
	"testing"

	"cursor-proxy/internal/config"
)

func TestSimulatedCacheUsage(t *testing.T) {
	const input, output = 13, 7
	for _, tc := range []struct {
		name, percent string
		enabled       bool
		cached        int
	}{
		{name: "default"},
		{name: "disabled", percent: "0"},
		{name: "invalid", percent: "80%"},
		{name: "out of range", percent: "101"},
		{name: "negative", percent: "-1"},
		{name: "rounded down to zero", percent: "1", enabled: true},
		{name: "partial", percent: "80", enabled: true, cached: 10},
		{name: "full", percent: "100", enabled: true, cached: input},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_TOKEN", "test-token")
			t.Setenv("SIMULATED_CACHE_PERCENT", tc.percent)
			config.Reset()
			t.Cleanup(config.Reset)

			wantOpenAI := map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}
			wantAnthropic := map[string]any{"input_tokens": input - tc.cached, "output_tokens": output}
			if tc.enabled {
				wantOpenAI["prompt_tokens_details"] = map[string]int{"cached_tokens": tc.cached}
				wantAnthropic["cache_read_input_tokens"] = tc.cached
				wantAnthropic["cache_creation_input_tokens"] = 0
			}
			if got := usagePayload(input, output); !reflect.DeepEqual(got, wantOpenAI) {
				t.Errorf("OpenAI usage = %v, want %v", got, wantOpenAI)
			}
			if got := anthropicUsage(input, output); !reflect.DeepEqual(got, wantAnthropic) {
				t.Errorf("Anthropic usage = %v, want %v", got, wantAnthropic)
			}
		})
	}
}
