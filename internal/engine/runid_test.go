package engine

import (
	"bytes"
	"io"
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

func TestNewRunIDRejectsUnusableEntropy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 27, 12, 34, 56, 0, time.UTC)
	for _, test := range []struct {
		name      string
		reader    io.Reader
		wantError string
	}{
		// A short read must fail rather than produce a zero-padded suffix.
		{name: "short read", reader: bytes.NewReader([]byte{0xde, 0xad}), wantError: "read random suffix"},
		{name: "nil reader", wantError: "nil reader"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewRunID(now, test.reader); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}
