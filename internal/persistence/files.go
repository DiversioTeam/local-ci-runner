package persistence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

func WriteJSONFile[T any](path string, value T) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json for %s: %w", path, err)
	}

	payload = append(payload, '\n')
	if err := writeFileAtomic(path, payload); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func ReadJSONFile[T any](path string) (T, error) {
	var value T

	payload, err := os.ReadFile(path)
	if err != nil {
		return value, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(payload, &value); err != nil {
		return value, fmt.Errorf("decode %s: %w", path, err)
	}

	return value, nil
}

func WriteTextFile(path string, content string) error {
	if err := writeFileAtomic(path, []byte(content)); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func WriteEnvFile(path string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		value := env[key]
		if err := validateEnvEntry(path, key, value); err != nil {
			return err
		}
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(value)
		builder.WriteByte('\n')
	}

	if err := writeFileAtomic(path, []byte(builder.String())); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func ReadEnvFile(path string) (map[string]string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	text := strings.TrimRight(string(payload), "\r\n")
	if strings.TrimSpace(text) == "" {
		return map[string]string{}, nil
	}

	result := make(map[string]string)
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("parse %s: invalid env line %q", path, line)
		}
		if err := validateEnvEntry(path, key, value); err != nil {
			return nil, err
		}
		result[key] = value
	}

	return result, nil
}

func TouchFile(path string) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create parent for %s: %w", path, err)
	}

	file, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("touch %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}

	return nil
}

func validateEnvEntry(path string, key string, value string) error {
	if key == "" {
		return fmt.Errorf("parse %s: env key must not be empty", path)
	}
	if strings.Contains(key, "=") {
		return fmt.Errorf("parse %s: env key %q must not contain '='", path, key)
	}
	if strings.Contains(key, "\n") || strings.Contains(key, "\r") {
		return fmt.Errorf("parse %s: env key %q must not contain newlines", path, key)
	}
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf("parse %s: env key %q must match %s", path, key, envKeyPattern.String())
	}
	if strings.Contains(value, "\n") || strings.Contains(value, "\r") {
		return fmt.Errorf("parse %s: env value for %q must not contain newlines", path, key)
	}

	return nil
}

func writeFileAtomic(path string, data []byte) (err error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create parent for %s: %w", path, err)
	}

	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tempFile.Name())
		}
	}()

	if _, err = tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err = tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err = os.Rename(tempFile.Name(), path); err != nil {
		return fmt.Errorf("rename temp file for %s: %w", path, err)
	}

	return nil
}
