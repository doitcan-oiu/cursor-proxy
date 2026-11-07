package store

import (
	"os"
	"sync"
	"testing"

	"cursor-proxy/internal/config"
)

func TestSimulatedCachePersistence(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_TOKEN", "test-admin")
	t.Setenv("SIMULATED_CACHE_PERCENT", "80")
	config.Reset()
	t.Cleanup(config.Reset)
	resetStore := func() {
		mu.Lock()
		defer mu.Unlock()
		data = nil
		loadOnce = sync.Once{}
	}
	resetStore()
	t.Cleanup(resetStore)

	if err := os.WriteFile(filePath(), []byte(`{"cursorTokens":[{"id":"existing"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := GetSimulatedCache(); got != (SimulatedCacheSettings{ReadPercent: 80}) {
		t.Fatalf("legacy config = %+v, want 80%% read and 0%% write", got)
	}

	settings := SimulatedCacheSettings{ReadPercent: 60, WritePercent: 25}
	if err := SetSimulatedCache(settings); err != nil {
		t.Fatal(err)
	}
	if got := GetSimulatedCache(); got != settings {
		t.Fatalf("live config = %+v, want %+v", got, settings)
	}
	resetStore()
	if got := GetSimulatedCache(); got != settings {
		t.Fatalf("reloaded config = %+v, want %+v", got, settings)
	}
	if tokens := Read().CursorTokens; len(tokens) != 1 || tokens[0].ID != "existing" {
		t.Fatalf("save changed existing accounts: %+v", tokens)
	}

	disabled := SimulatedCacheSettings{}
	if err := SetSimulatedCache(disabled); err != nil {
		t.Fatal(err)
	}
	resetStore()
	if got := GetSimulatedCache(); got != disabled {
		t.Fatalf("reloaded disabled config = %+v, want explicit 0/0", got)
	}

	if err := os.Rename(filePath(), filePath()+".bak"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SetSimulatedCache(settings); err == nil {
		t.Fatal("save should fail when store path is a directory")
	}
	if got := GetSimulatedCache(); got != disabled {
		t.Fatalf("failed save changed live config: %+v", got)
	}
}
