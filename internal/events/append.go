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
	path              string
	runID             string
	nextSequence      int64
	PublicationSource PublicationSource
}

func NewAppender(path string, runID string) (Appender, error) {
	nextSequence, err := nextSequence(path)
	if err != nil {
		return Appender{}, err
	}

	return Appender{
		path:              path,
		runID:             runID,
		nextSequence:      nextSequence,
		PublicationSource: PublicationExecution,
	}, nil
}

func (appender *Appender) Append(now time.Time, eventType Type, stepID string, status string, message string) error {
	return appender.addEvent(Event{Time: now.UTC(), Type: eventType, StepID: stepID, Status: status, Message: message})
}

func (appender *Appender) AddGitHubPost(now time.Time, eventType Type, stepID, status string, post GitHubPost) error {
	return appender.addEvent(Event{Time: now.UTC(), Type: eventType, StepID: stepID, Status: status,
		Message: post.Context + "=" + status, GitHubPost: &post})
}

func (appender *Appender) addEvent(event Event) error {
	file, err := os.OpenFile(appender.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", appender.path, err)
	}
	defer func() {
		_ = file.Close()
	}()

	event.Sequence = appender.nextSequence
	event.RunID = appender.runID
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	payload = append(payload, '\n')
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write %s: %w", appender.path, err)
	}

	// A durable intent must exist before the network call; acknowledgement must also reach disk.
	if event.GitHubPost != nil {
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync publication event: %w", err)
		}
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

	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, info.Size()-1); err != nil {
			return 0, err
		}
		if last[0] != '\n' {
			return 0, fmt.Errorf("event log has an incomplete trailing line; refusing to append")
		}
	}
	if _, err := ReadFile(path); err != nil {
		return 0, err
	}
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
