package events

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Sequence is the persistence order, so it must continue when resume or publish reopens the log.
func TestAppenderContinuesSequenceAcrossWriters(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "events.jsonl")
	at := time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC)
	for _, eventTypes := range [][]Type{{RunStarted, StepStarted}, {RunStarted}} {
		appender, err := NewAppender(path, "run-1")
		if err != nil {
			t.Fatal(err)
		}
		for _, eventType := range eventTypes {
			if err := appender.Append(at, eventType, "", "pending", ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	items, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for index, item := range items {
		if item.Sequence != int64(index+1) {
			t.Fatalf("event %d has sequence %d", index, item.Sequence)
		}
	}
	if len(items) != 3 {
		t.Fatalf("event count = %d, want 3", len(items))
	}
}

// Readers tolerate a torn tail during an active write; writers must never build on one.
func TestAppenderRefusesTornTail(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"github_post":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppender(path, "run-1"); err == nil {
		t.Fatal("writer accepted a torn event log")
	}
}
