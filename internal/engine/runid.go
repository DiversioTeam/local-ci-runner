package engine

import (
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

func NewRunID(now time.Time, random io.Reader) (string, error) {
	if random == nil {
		return "", fmt.Errorf("read random suffix: nil reader")
	}

	buf := make([]byte, 4)
	if _, err := io.ReadFull(random, buf); err != nil {
		return "", fmt.Errorf("read random suffix: %w", err)
	}

	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(buf), nil
}
