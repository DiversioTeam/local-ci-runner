package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNewRunID(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 27, 12, 34, 56, 0, time.UTC)
	id, err := NewRunID(now, bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef}))
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}

	const want = "20260627T123456Z-deadbeef"
	if id != want {
		t.Fatalf("id = %q, want %q", id, want)
	}
}

func TestNewRunIDRejectsBadReader(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 27, 12, 34, 56, 0, time.UTC)
	_, err := NewRunID(now, bytes.NewReader([]byte{0xde, 0xad}))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "read random suffix") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewRunIDRejectsNilReader(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 27, 12, 34, 56, 0, time.UTC)
	_, err := NewRunID(now, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil reader") {
		t.Fatalf("error = %v", err)
	}
}
