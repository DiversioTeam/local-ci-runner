package events

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReadFile supports live inspection of events.jsonl while another process is still
// appending to it.
//
// First principle: the event log is append-only, so a reader should accept every
// complete line that already landed on disk. The one unsafe thing to assume is that
// the last line is complete, because the writer may be mid-write when we open the file.
//
// To keep `local-ci logs <run-id>` and `local-ci show <run-id>` stable for active
// runs, we parse all complete lines and silently ignore one partial trailing line.
func ReadFile(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	items := make([]Event, 0)
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		if readErr == io.EOF && line == "" {
			break
		}

		lineNumber++
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var event Event
			if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("decode %s line %d: %w", path, lineNumber, err)
			}
			items = append(items, event)
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	return items, nil
}
