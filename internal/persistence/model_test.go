package persistence

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// Unset optional fields are absent, not null or zero (MANUAL §8). Comparing the exact key
// set keeps a new field from silently defaulting into every record.
func TestUnsetOptionalFieldsAreOmitted(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		record any
		want   []string
	}{
		{name: "meta", record: Meta{}, want: []string{"config_hash", "config_path", "created_at", "head_sha", "plan_hash", "repo_root", "repo_slug", "run_id"}},
		{name: "step status", record: StepStatus{}, want: []string{"index", "state", "step_id"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.record)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, test.want) {
				t.Fatalf("keys = %v, want %v", got, test.want)
			}
		})
	}
}
