package openai

import (
	"reflect"
	"testing"

	"cursor-proxy/internal/store"
)

func TestSimulatedCacheUsage(t *testing.T) {
	const input, output = 13, 7
	for _, tc := range []struct {
		name          string
		settings      store.SimulatedCacheSettings
		enabled       bool
		read, written int
	}{
		{name: "disabled"},
		{name: "rounded down to zero", settings: store.SimulatedCacheSettings{ReadPercent: 1, WritePercent: 1}, enabled: true},
		{name: "split and round down", settings: store.SimulatedCacheSettings{ReadPercent: 80, WritePercent: 20}, enabled: true, read: 10, written: 2},
		{name: "full read", settings: store.SimulatedCacheSettings{ReadPercent: 100}, enabled: true, read: input},
		{name: "full write", settings: store.SimulatedCacheSettings{WritePercent: 100}, enabled: true, written: input},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantOpenAI := map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}
			wantAnthropic := map[string]any{"input_tokens": input - tc.read - tc.written, "output_tokens": output}
			if tc.enabled {
				wantOpenAI["prompt_tokens_details"] = map[string]int{"cached_tokens": tc.read, "cache_write_tokens": tc.written}
				wantAnthropic["cache_read_input_tokens"] = tc.read
				wantAnthropic["cache_creation_input_tokens"] = tc.written
			}
			usage := splitInputUsage(input, tc.settings)
			if got := usage.openAI(output); !reflect.DeepEqual(got, wantOpenAI) {
				t.Errorf("OpenAI usage = %v, want %v", got, wantOpenAI)
			}
			if got := usage.anthropic(output); !reflect.DeepEqual(got, wantAnthropic) {
				t.Errorf("Anthropic usage = %v, want %v", got, wantAnthropic)
			}
		})
	}
}
