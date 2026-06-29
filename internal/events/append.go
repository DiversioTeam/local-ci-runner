package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type Appender struct {
	path         string
	runID        string
	nextSequence int64
}

func NewAppender(path string, runID string) (Appender, error) {
	nextSequence, err := nextSequence(path)
	if err != nil {
		return Appender{}, err
	}

	return Appender{
		path:         path,
		runID:        runID,
		nextSequence: nextSequence,
	}, nil
}

func (appender *Appender) Append(now time.Time, eventType Type, stepID string, status string, message string) error {
	file, err := os.OpenFile(appender.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", appender.path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	event := Event{
		Sequence: appender.nextSequence,
		Time:     now.UTC(),
		RunID:    appender.runID,
		Type:     eventType,
		StepID:   stepID,
		Status:   status,
		Message:  message,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write %s: %w", appender.path, err)
	}

	appender.nextSequence++
	return nil
}

func nextSequence(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	var count int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", path, err)
	}

	return count + 1, nil
}
