package persistence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The on-disk layout is a documented contract (docs/contracts.md §4), so expected
// paths are literals rather than values built from the constants under test.
func TestStorePaths(t *testing.T) {
	t.Parallel()

	store := NewStore("/repo")
	for _, test := range []struct{ got, want string }{
		{store.RunsRoot(), "/repo/.local-ci/runs"},
		{store.RunFile("run-1", MetaFile), "/repo/.local-ci/runs/run-1/meta.json"},
		{store.RunFile("run-1", PlanFile), "/repo/.local-ci/runs/run-1/plan.json"},
		{store.RunFile("run-1", PlanEnvFile), "/repo/.local-ci/runs/run-1/plan.env"},
		{store.RunFile("run-1", SummaryFile), "/repo/.local-ci/runs/run-1/summary.json"},
		{store.RunFile("run-1", SummaryText), "/repo/.local-ci/runs/run-1/summary.txt"},
		{store.RunFile("run-1", EventsFile), "/repo/.local-ci/runs/run-1/events.jsonl"},
		{store.RunFile("run-1", PlannerLogFile), "/repo/.local-ci/runs/run-1/planner.log"},
		{store.StepFile("run-1", 0, "lint", StatusFile), "/repo/.local-ci/runs/run-1/steps/001-lint/status.json"},
		{store.StepFile("run-1", 9, "x", OutputEnv), "/repo/.local-ci/runs/run-1/steps/010-x/output.env"},
		{store.StepFile("run-1", 0, "lint", StdoutLog), "/repo/.local-ci/runs/run-1/steps/001-lint/stdout.log"},
		{store.StepFile("run-1", 0, "lint", StderrLog), "/repo/.local-ci/runs/run-1/steps/001-lint/stderr.log"},
		{store.StepFile("run-1", 0, "lint", CombinedLog), "/repo/.local-ci/runs/run-1/steps/001-lint/combined.log"},
		// status.json stores log paths relative to the run directory.
		{StepRelPath(0, "lint", StdoutLog), "steps/001-lint/stdout.log"},
	} {
		if test.got != test.want {
			t.Errorf("path = %q, want %q", test.got, test.want)
		}
	}
}

func TestEnvFileRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), PlanEnvFile)
	if err := WriteEnvFile(path, map[string]string{"B": "2", "A": "1", "EMPTY": ""}); err != nil {
		t.Fatalf("WriteEnvFile() error = %v", err)
	}
	// Sorted keys keep plan.env byte-stable for hashing and review.
	if payload, err := os.ReadFile(path); err != nil || string(payload) != "A=1\nB=2\nEMPTY=\n" {
		t.Fatalf("plan.env = %q, error = %v", payload, err)
	}
	for name, content := range map[string]string{"LF": "A=1\nB=2\nEMPTY=\n", "CRLF": "A=1\r\nB=2\r\nEMPTY=\r\n"} {
		if err := WriteTextFile(path, content); err != nil {
			t.Fatal(err)
		}
		loaded, err := ReadEnvFile(path)
		if err != nil || len(loaded) != 3 || loaded["A"] != "1" || loaded["B"] != "2" || loaded["EMPTY"] != "" {
			t.Fatalf("%s: loaded = %v, error = %v", name, loaded, err)
		}
	}
}

// Steps write output.env themselves, so the reader enforces the same rules as the writer.
func TestEnvFileRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// write is the entry WriteEnvFile must refuse; read is the line ReadEnvFile must refuse.
		write     map[string]string
		read      string
		wantError string
	}{
		{name: "dash in key", write: map[string]string{"BAD-KEY": "1"}, read: "BAD-KEY=1", wantError: `"BAD-KEY" must match`},
		{name: "leading digit", write: map[string]string{"1A": "1"}, read: "1A=1", wantError: `"1A" must match`},
		{name: "empty key", write: map[string]string{"": "1"}, read: "=1", wantError: "must not be empty"},
		{name: "newline in value", write: map[string]string{"A": "1\nB=2"}, wantError: "must not contain newlines"},
		{name: "line without separator", read: "noequals", wantError: `invalid env line "noequals"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			if test.write != nil {
				path := filepath.Join(directory, PlanEnvFile)
				if err := WriteEnvFile(path, test.write); err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("write error = %v, want %q", err, test.wantError)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("rejected env file was still written")
				}
			}
			if test.read != "" {
				path := filepath.Join(directory, OutputEnv)
				if err := WriteTextFile(path, test.read+"\n"); err != nil {
					t.Fatal(err)
				}
				if _, err := ReadEnvFile(path); err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("read error = %v, want %q", err, test.wantError)
				}
			}
		})
	}
}
