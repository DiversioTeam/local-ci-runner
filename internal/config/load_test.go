package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStaticConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `version = 1

[github]
enabled = true

[[steps]]
id = "fmt"
command = ["go", "fmt", "./..."]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got, want := cfg.GitHub.AggregateContext, DefaultAggregateContext; got != want {
		t.Fatalf("aggregate context = %q, want %q", got, want)
	}
	if len(cfg.Steps) != 1 {
		t.Fatalf("step count = %d, want 1", len(cfg.Steps))
	}
	if got, want := cfg.Steps[0].Name, "fmt"; got != want {
		t.Fatalf("step name = %q, want %q", got, want)
	}
	if got, want := cfg.Steps[0].Dir, DefaultWorkingDir; got != want {
		t.Fatalf("step dir = %q, want %q", got, want)
	}
	if got, want := cfg.Steps[0].GitHubContext, "local/fmt"; got != want {
		t.Fatalf("step context = %q, want %q", got, want)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := writeTempConfig(t, `version = 1

[github]
enabled = true
bogus = "nope"

[[steps]]
id = "fmt"
command = ["go", "fmt", "./..."]
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown fields") {
		t.Fatalf("error = %v, want unknown fields", err)
	}
}

func TestValidateRejectsBadNeeds(t *testing.T) {
	t.Parallel()

	cfg := File{
		Version: 1,
		Steps: []Step{{
			ID:      "test",
			Command: []string{"go", "test", "./..."},
			Needs:   []string{"fmt"},
		}},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `needs unknown step "fmt"`) {
		t.Fatalf("error = %v", err)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}
