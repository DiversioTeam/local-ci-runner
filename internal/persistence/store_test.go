package persistence

import (
	"path/filepath"
	"testing"
)

func TestStorePaths(t *testing.T) {
	t.Parallel()

	store := NewStore("/repo")

	if got, want := store.RunsRoot(), filepath.Join("/repo", RunsDir); got != want {
		t.Fatalf("RunsRoot() = %q, want %q", got, want)
	}
	if got, want := store.RunFile("run-1", MetaFile), filepath.Join("/repo", RunsDir, "run-1", MetaFile); got != want {
		t.Fatalf("RunFile() = %q, want %q", got, want)
	}
	if got, want := store.StepFile("run-1", 0, "lint", StatusFile), filepath.Join("/repo", RunsDir, "run-1", StepsDir, "001-lint", StatusFile); got != want {
		t.Fatalf("StepFile() = %q, want %q", got, want)
	}
}

func TestWriteAndReadEnvFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), PlanEnvFile)
	env := map[string]string{
		"B": "2",
		"A": "1",
	}

	if err := WriteEnvFile(path, env); err != nil {
		t.Fatalf("WriteEnvFile() error = %v", err)
	}

	loaded, err := ReadEnvFile(path)
	if err != nil {
		t.Fatalf("ReadEnvFile() error = %v", err)
	}
	if got, want := loaded["A"], "1"; got != want {
		t.Fatalf("env[A] = %q, want %q", got, want)
	}
	if got, want := loaded["B"], "2"; got != want {
		t.Fatalf("env[B] = %q, want %q", got, want)
	}
}

func TestWriteEnvFileRejectsInvalidEntries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), PlanEnvFile)
	env := map[string]string{
		"BAD\nKEY": "1",
	}

	err := WriteEnvFile(path, env)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReadEnvFileHandlesCRLF(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), PlanEnvFile)
	if err := WriteTextFile(path, "A=1\r\nB=2\r\n"); err != nil {
		t.Fatalf("WriteTextFile() error = %v", err)
	}

	loaded, err := ReadEnvFile(path)
	if err != nil {
		t.Fatalf("ReadEnvFile() error = %v", err)
	}
	if got, want := loaded["A"], "1"; got != want {
		t.Fatalf("env[A] = %q, want %q", got, want)
	}
	if got, want := loaded["B"], "2"; got != want {
		t.Fatalf("env[B] = %q, want %q", got, want)
	}
}
