package config

import (
	"strings"
	"testing"
)

func TestValidateRejectsPlannerAndStepsTogether(t *testing.T) {
	t.Parallel()

	cfg := File{
		Version: 1,
		Planner: &Planner{Command: []string{"./scripts/plan.sh"}},
		Steps:   []Step{{ID: "lint", Command: []string{"./scripts/lint.sh"}}},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "either planner or steps") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsDependencyCycle(t *testing.T) {
	t.Parallel()

	cfg := File{
		Version: 1,
		Steps: []Step{
			{ID: "lint", Command: []string{"./scripts/lint.sh"}, Needs: []string{"test"}},
			{ID: "test", Command: []string{"./scripts/test.sh"}, Needs: []string{"lint"}},
		},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "dependency cycle detected") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsUnsupportedIf(t *testing.T) {
	t.Parallel()

	cfg := File{
		Version: 1,
		Steps: []Step{{
			ID:      "lint",
			Command: []string{"./scripts/lint.sh"},
			If:      "env.CHANGED == 'python'",
		}},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `if must be empty, "true", or "false"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRejectsEmptyExecutable(t *testing.T) {
	t.Parallel()

	cfg := File{
		Version: 1,
		Steps: []Step{{
			ID:      "lint",
			Command: []string{""},
		}},
	}
	cfg.ApplyDefaults()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "non-empty executable") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolvedPlanValidateAllowsEmptyStepList(t *testing.T) {
	t.Parallel()

	plan := ResolvedPlan{}
	plan.ApplyDefaults()

	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
