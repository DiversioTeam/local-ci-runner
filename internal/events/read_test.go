package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadFileIgnoresPartialTrailingLine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	content := "{\"sequence\":1,\"time\":\"2026-06-29T00:00:00Z\",\"run_id\":\"run-1\",\"type\":\"run.started\"}\n{"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	items, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("event count = %d, want 1", len(items))
	}
	if got, want := items[0].Type, RunStarted; got != want {
		t.Fatalf("event type = %q, want %q", got, want)
	}
	if got, want := items[0].Time, time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("event time = %v, want %v", got, want)
	}
}
