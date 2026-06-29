package persistence

import (
	"path/filepath"
	"testing"
)

func TestStepDirName(t *testing.T) {
	t.Parallel()

	if got, want := StepDirName(0, "lint"), "001-lint"; got != want {
		t.Fatalf("StepDirName() = %q, want %q", got, want)
	}
}

func TestStepRelPath(t *testing.T) {
	t.Parallel()

	if got, want := StepRelPath(0, "lint", StatusFile), filepath.Join(StepsDir, "001-lint", StatusFile); got != want {
		t.Fatalf("StepRelPath() = %q, want %q", got, want)
	}
}
