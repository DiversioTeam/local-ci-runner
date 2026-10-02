package config

import "testing"

func TestResolvedPlanValidateAllowsEmptyStepList(t *testing.T) {
	t.Parallel()

	plan := ResolvedPlan{}
	plan.ApplyDefaults()

	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
