package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func TestExecuteRunStreamsProgressAndProcessOutput(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{{
		ID:      "echo",
		Command: []string{"/bin/sh", "-c", "printf 'HELLO\\n'; printf 'ERR\\n' >&2"},
	}}}
	plan.ApplyDefaults()

	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var progress bytes.Buffer
	_, err := ExecuteRun(fixture.store, run, ExecuteOptions{
		Stdout:   &stdout,
		Stderr:   &stderr,
		Progress: &progress,
	})
	if err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "HELLO") {
		t.Fatalf("stdout = %q, want HELLO", got)
	}
	if got := stderr.String(); !strings.Contains(got, "ERR") {
		t.Fatalf("stderr = %q, want ERR", got)
	}
	progressText := progress.String()
	for _, want := range []string{"run ", "start echo", "ok echo", "finished: success"} {
		if !strings.Contains(progressText, want) {
			t.Fatalf("progress = %q, want substring %q", progressText, want)
		}
	}
}

func TestExecuteRunCapturesLogsEventsAndBlockedSteps(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{
		Env: map[string]string{"CHANGED_SCOPE": "python"},
		Steps: []config.Step{
			{
				ID:      "prep",
				Command: []string{"/bin/sh", "-c", "printf 'HELLO\n'; printf 'ERR\n' >&2; printf 'RESULT=ok\nSTEP=%s\nSCOPE=%s\nRUN_ID=%s\n' \"$LOCAL_CI_STEP_ID\" \"$CHANGED_SCOPE\" \"$LOCAL_CI_RUN_ID\" > \"$LOCAL_CI_STEP_OUTPUT\""},
			},
			{
				ID:      "fail",
				Needs:   []string{"prep"},
				Command: []string{"/bin/sh", "-c", "printf 'bad\n'; printf 'boom\n' >&2; exit 3"},
			},
			{
				ID:      "after",
				Needs:   []string{"fail"},
				Command: []string{"/bin/sh", "-c", "printf 'should-not-run\n'"},
			},
		}}
	plan.ApplyDefaults()

	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)

	executed, err := ExecuteRun(fixture.store, run, ExecuteOptions{})
	if err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}

	states := statusStates(executed.StepStatuses)
	if got, want := states["prep"], string(StepStateSuccess); got != want {
		t.Fatalf("prep state = %q, want %q", got, want)
	}
	if got, want := states["fail"], string(StepStateFailure); got != want {
		t.Fatalf("fail state = %q, want %q", got, want)
	}
	if got, want := states["after"], string(StepStateBlocked); got != want {
		t.Fatalf("after state = %q, want %q", got, want)
	}
	if got, want := executed.Summary.Status, string(StepStateFailure); got != want {
		t.Fatalf("summary status = %q, want %q", got, want)
	}
	if executed.Meta.FinishedAt == nil {
		t.Fatal("expected finished time")
	}
	if executed.Meta.RunnerPID == nil || *executed.Meta.RunnerPID <= 0 {
		t.Fatalf("expected runner pid, got %v", executed.Meta.RunnerPID)
	}

	prepStdout := mustReadFile(t, fixture.store.StepFile(run.RunID, 0, "prep", persistence.StdoutLog))
	if !strings.Contains(prepStdout, "HELLO") {
		t.Fatalf("prep stdout missing HELLO: %q", prepStdout)
	}
	prepStderr := mustReadFile(t, fixture.store.StepFile(run.RunID, 0, "prep", persistence.StderrLog))
	if !strings.Contains(prepStderr, "ERR") {
		t.Fatalf("prep stderr missing ERR: %q", prepStderr)
	}
	prepCombined := mustReadFile(t, fixture.store.StepFile(run.RunID, 0, "prep", persistence.CombinedLog))
	if !strings.Contains(prepCombined, "HELLO") || !strings.Contains(prepCombined, "ERR") {
		t.Fatalf("prep combined log missing output: %q", prepCombined)
	}
	prepEnv, err := persistence.ReadEnvFile(fixture.store.StepFile(run.RunID, 0, "prep", persistence.OutputEnv))
	if err != nil {
		t.Fatalf("ReadEnvFile() error = %v", err)
	}
	if got, want := prepEnv["RESULT"], "ok"; got != want {
		t.Fatalf("RESULT = %q, want %q", got, want)
	}
	if got, want := prepEnv["STEP"], "prep"; got != want {
		t.Fatalf("STEP = %q, want %q", got, want)
	}
	if got, want := prepEnv["SCOPE"], "python"; got != want {
		t.Fatalf("SCOPE = %q, want %q", got, want)
	}
	if got, want := prepEnv["RUN_ID"], run.RunID; got != want {
		t.Fatalf("RUN_ID = %q, want %q", got, want)
	}

	failStatus := findStatus(t, executed.StepStatuses, "fail")
	if failStatus.ExitCode == nil || *failStatus.ExitCode != 3 {
		t.Fatalf("fail exit code = %v, want 3", failStatus.ExitCode)
	}
	failCombined := mustReadFile(t, fixture.store.StepFile(run.RunID, 1, "fail", persistence.CombinedLog))
	if !strings.Contains(failCombined, "bad") || !strings.Contains(failCombined, "boom") || !strings.Contains(failCombined, "exit code 3") {
		t.Fatalf("fail combined log missing output: %q", failCombined)
	}
	blockedStdout := mustReadFile(t, fixture.store.StepFile(run.RunID, 2, "after", persistence.StdoutLog))
	if blockedStdout != "" {
		t.Fatalf("blocked stdout = %q, want empty", blockedStdout)
	}

	eventItems := mustReadEvents(t, fixture.store.RunFile(run.RunID, persistence.EventsFile))
	wantTypes := []events.Type{
		events.RunStarted,
		events.StepStarted,
		events.StepFinished,
		events.StepStarted,
		events.StepFinished,
		events.StepBlocked,
		events.RunFinished,
	}
	if len(eventItems) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d", len(eventItems), len(wantTypes))
	}
	for index, wantType := range wantTypes {
		if got := eventItems[index].Type; got != wantType {
			t.Fatalf("event %d type = %q, want %q", index, got, wantType)
		}
		if got, want := eventItems[index].Sequence, int64(index+1); got != want {
			t.Fatalf("event %d sequence = %d, want %d", index, got, want)
		}
	}
	if got, want := eventItems[len(eventItems)-1].Status, string(StepStateFailure); got != want {
		t.Fatalf("run finished status = %q, want %q", got, want)
	}
}

func TestExecuteRunHonorsTopologicalOrderAndSkipConditions(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{
		{
			ID:      "later",
			Needs:   []string{"first"},
			Command: []string{"/bin/sh", "-c", "printf 'later\n' >> \"$LOCAL_CI_REPO_ROOT/order.log\""},
		},
		{
			ID:      "skip",
			If:      "false",
			Command: []string{"/bin/sh", "-c", "printf 'skip\n' >> \"$LOCAL_CI_REPO_ROOT/order.log\""},
		},
		{
			ID:      "first",
			Command: []string{"/bin/sh", "-c", "printf 'first\n' >> \"$LOCAL_CI_REPO_ROOT/order.log\""},
		},
	}}
	plan.ApplyDefaults()

	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)

	executed, err := ExecuteRun(fixture.store, run, ExecuteOptions{})
	if err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}

	orderLog := mustReadFile(t, filepath.Join(fixture.repoRoot, "order.log"))
	if got, want := orderLog, "first\nlater\n"; got != want {
		t.Fatalf("order log = %q, want %q", got, want)
	}

	states := statusStates(executed.StepStatuses)
	if got, want := states["skip"], string(StepStateSkipped); got != want {
		t.Fatalf("skip state = %q, want %q", got, want)
	}
	if got, want := states["first"], string(StepStateSuccess); got != want {
		t.Fatalf("first state = %q, want %q", got, want)
	}
	if got, want := states["later"], string(StepStateSuccess); got != want {
		t.Fatalf("later state = %q, want %q", got, want)
	}
}

func TestExecuteRunPostsGitHubStatuses(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{
		{
			ID:      "ok",
			Command: []string{"/bin/sh", "-c", "printf 'ok\n'"},
		},
		{
			ID:      "skip",
			If:      "false",
			Command: []string{"/bin/sh", "-c", "printf 'skip\n'"},
		},
		{
			ID:      "fail",
			Needs:   []string{"ok"},
			Command: []string{"/bin/sh", "-c", "exit 4"},
		},
		{
			ID:      "blocked",
			Needs:   []string{"fail"},
			Command: []string{"/bin/sh", "-c", "printf 'blocked\n'"},
		},
	}}
	plan.ApplyDefaults()

	fixture := newRunFixtureWithGitHub(t, plan, config.GitHub{Enabled: true, AggregateContext: config.DefaultAggregateContext})
	run := prepareRunFixture(t, fixture)
	reporter := &fakeReporter{}

	_, err := ExecuteRun(fixture.store, run, ExecuteOptions{Reporter: reporter})
	if err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}

	got := make([]string, 0, len(reporter.posts))
	for _, post := range reporter.posts {
		got = append(got, post.status.Context+"="+string(post.status.State))
	}
	want := []string{
		"local/verify=pending",
		"local/ok=pending",
		"local/ok=success",
		"local/skip=success",
		"local/fail=pending",
		"local/fail=failure",
		"local/blocked=failure",
		"local/verify=failure",
	}
	assertStatusPosts(t, got, want)
}

func TestExecuteRunResumeReusesSuccessfulSteps(t *testing.T) {
	t.Parallel()

	plan := config.ResolvedPlan{Steps: []config.Step{
		{
			ID:      "lint",
			Command: []string{"/bin/sh", "-c", "printf 'lint\n' >> \"$LOCAL_CI_REPO_ROOT/reuse.log\""},
		},
		{
			ID:      "flaky",
			Needs:   []string{"lint"},
			Command: []string{"/bin/sh", "-c", "if [ ! -f \"$LOCAL_CI_REPO_ROOT/flaky.flag\" ]; then touch \"$LOCAL_CI_REPO_ROOT/flaky.flag\"; printf 'first\n' >&2; exit 2; fi; printf 'second\n'"},
		},
		{
			ID:      "package",
			Needs:   []string{"flaky"},
			Command: []string{"/bin/sh", "-c", "printf 'package\n' >> \"$LOCAL_CI_REPO_ROOT/reuse.log\""},
		},
	}}
	plan.ApplyDefaults()

	fixture := newRunFixture(t, plan)
	run := prepareRunFixture(t, fixture)

	firstRun, err := ExecuteRun(fixture.store, run, ExecuteOptions{})
	if err != nil {
		t.Fatalf("first ExecuteRun() error = %v", err)
	}
	if got, want := firstRun.Summary.Status, string(StepStateFailure); got != want {
		t.Fatalf("first summary status = %q, want %q", got, want)
	}

	loaded, err := LoadRunForResume(fixture.store, run.RunID, fixture.identity)
	if err != nil {
		t.Fatalf("LoadRunForResume() error = %v", err)
	}
	secondRun, err := ExecuteRun(fixture.store, loaded, ExecuteOptions{})
	if err != nil {
		t.Fatalf("second ExecuteRun() error = %v", err)
	}
	if got, want := secondRun.Summary.Status, string(StepStateSuccess); got != want {
		t.Fatalf("second summary status = %q, want %q", got, want)
	}

	states := statusStates(secondRun.StepStatuses)
	if got, want := states["lint"], string(StepStateSuccess); got != want {
		t.Fatalf("lint state = %q, want %q", got, want)
	}
	if got, want := states["flaky"], string(StepStateSuccess); got != want {
		t.Fatalf("flaky state = %q, want %q", got, want)
	}
	if got, want := states["package"], string(StepStateSuccess); got != want {
		t.Fatalf("package state = %q, want %q", got, want)
	}

	reuseLog := mustReadFile(t, filepath.Join(fixture.repoRoot, "reuse.log"))
	if got, want := reuseLog, "lint\npackage\n"; got != want {
		t.Fatalf("reuse log = %q, want %q", got, want)
	}

	eventItems := mustReadEvents(t, fixture.store.RunFile(run.RunID, persistence.EventsFile))
	if len(eventItems) < 10 {
		t.Fatalf("expected appended resume events, got %d", len(eventItems))
	}
	secondRunStarted := findNthEventOfType(t, eventItems, events.RunStarted, 2)
	if got, want := secondRunStarted.Status, runStatusPending; got != want {
		t.Fatalf("second run started status = %q, want %q", got, want)
	}
	if got, want := eventItems[len(eventItems)-1].Type, events.RunFinished; got != want {
		t.Fatalf("last event type = %q, want %q", got, want)
	}
	if got, want := eventItems[len(eventItems)-1].Status, string(StepStateSuccess); got != want {
		t.Fatalf("last event status = %q, want %q", got, want)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(payload)
}

func mustReadEvents(t *testing.T, path string) []events.Event {
	t.Helper()

	payload := mustReadFile(t, path)
	if strings.TrimSpace(payload) == "" {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(payload), "\n")
	items := make([]events.Event, 0, len(lines))
	for _, line := range lines {
		var item events.Event
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("Unmarshal(%q) error = %v", line, err)
		}
		items = append(items, item)
	}
	return items
}

func findNthEventOfType(t *testing.T, items []events.Event, wantType events.Type, ordinal int) events.Event {
	t.Helper()

	seen := 0
	for _, item := range items {
		if item.Type != wantType {
			continue
		}
		seen++
		if seen == ordinal {
			return item
		}
	}
	t.Fatalf("missing event %q #%d", wantType, ordinal)
	return events.Event{}
}

func findStatus(t *testing.T, statuses []persistence.StepStatus, stepID string) persistence.StepStatus {
	t.Helper()

	for _, status := range statuses {
		if status.StepID == stepID {
			return status
		}
	}
	t.Fatalf("missing step status %q", stepID)
	return persistence.StepStatus{}
}

func assertStatusPosts(t *testing.T, got []string, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("post count = %d, want %d (%v)", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("post[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func statusStates(statuses []persistence.StepStatus) map[string]string {
	states := make(map[string]string, len(statuses))
	for _, status := range statuses {
		states[status.StepID] = status.State
	}
	return states
}
