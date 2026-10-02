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

// Engine applies defaults to a clone; the caller's plan must not change underneath it.
func TestResolvedPlanCloneIsDeep(t *testing.T) {
	t.Parallel()

	original := ResolvedPlan{
		Env:   map[string]string{"SCOPE": "python"},
		Steps: []Step{{ID: "lint", Command: []string{"./lint.sh"}, Needs: []string{"fmt"}, Env: map[string]string{"FOO": "bar"}}},
	}
	clone := original.Clone()
	clone.Env["SCOPE"] = "js"
	clone.Steps[0].Command[0] = "./other.sh"
	clone.Steps[0].Needs[0] = "test"
	clone.Steps[0].Env["FOO"] = "baz"
	clone.ApplyDefaults()

	if original.Env["SCOPE"] != "python" || original.Steps[0].Command[0] != "./lint.sh" || original.Steps[0].Needs[0] != "fmt" || original.Steps[0].Env["FOO"] != "bar" || original.Steps[0].Name != "" {
		t.Fatalf("clone shares state with the original: %+v", original)
	}
}
