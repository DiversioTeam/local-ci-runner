package config

import "testing"

func TestStaticPlanDeepCopiesSteps(t *testing.T) {
	t.Parallel()

	cfg := File{
		Steps: []Step{{
			ID:      "lint",
			Command: []string{"./scripts/lint.sh"},
			Needs:   []string{"fmt"},
			Env:     map[string]string{"FOO": "bar"},
		}},
	}

	plan := cfg.StaticPlan()
	plan.Steps[0].Command[0] = "./scripts/other.sh"
	plan.Steps[0].Needs[0] = "test"
	plan.Steps[0].Env["FOO"] = "baz"

	if got, want := cfg.Steps[0].Command[0], "./scripts/lint.sh"; got != want {
		t.Fatalf("original command = %q, want %q", got, want)
	}
	if got, want := cfg.Steps[0].Needs[0], "fmt"; got != want {
		t.Fatalf("original needs = %q, want %q", got, want)
	}
	if got, want := cfg.Steps[0].Env["FOO"], "bar"; got != want {
		t.Fatalf("original env = %q, want %q", got, want)
	}
}

func TestResolvedPlanApplyDefaults(t *testing.T) {
	t.Parallel()

	plan := ResolvedPlan{Steps: []Step{{
		ID:      "lint",
		Command: []string{"./scripts/lint.sh"},
	}}}

	plan.ApplyDefaults()

	if got, want := plan.Steps[0].Name, "lint"; got != want {
		t.Fatalf("step name = %q, want %q", got, want)
	}
	if got, want := plan.Steps[0].Dir, DefaultWorkingDir; got != want {
		t.Fatalf("step dir = %q, want %q", got, want)
	}
	if got, want := plan.Steps[0].GitHubContext, "local/lint"; got != want {
		t.Fatalf("step context = %q, want %q", got, want)
	}
}
