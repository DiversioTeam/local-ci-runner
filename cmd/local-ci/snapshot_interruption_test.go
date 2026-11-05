package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func TestSnapshotCreationInterruptFinishesThePersistedRun(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "local-ci")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, ".local-ci.toml"), []byte("version = 1\n[[steps]]\nid = 'check'\ncommand = ['true']\n"))
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrappers := t.TempDir()
	marker := filepath.Join(t.TempDir(), "snapshot-started")
	script := "#!/bin/sh\ncase \"$*\" in *'read-tree --reset -u'*) printf x > \"$LOCAL_CI_TEST_SNAPSHOT_STARTED\"; exec sleep 30;; esac\nexec \"$LOCAL_CI_TEST_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrappers, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "run", "--no-github")
	command.Dir = root
	command.Env = append(os.Environ(), "PATH="+wrappers+string(os.PathListSeparator)+os.Getenv("PATH"), "LOCAL_CI_TEST_REAL_GIT="+realGit, "LOCAL_CI_TEST_SNAPSHOT_STARTED="+marker)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	started := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("snapshot barrier not reached: %s", output.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- command.Wait() }()
	select {
	case err := <-finished:
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
			t.Fatalf("snapshot cancellation exit = %v, want 130; %s", err, output.String())
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-finished
		t.Fatal("snapshot cancellation did not finish")
	}
	store := persistence.NewStore(root)
	runIDs, err := store.ListRunIDs()
	if err != nil || len(runIDs) != 1 {
		t.Fatalf("runs = %v, %v", runIDs, err)
	}
	run, err := engine.LoadRun(store, runIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !run.Meta.Interrupted || run.Meta.FinishedAt == nil || run.Summary.Status != string(engine.StepStateInterrupted) {
		t.Fatalf("cancellation left unfinished evidence: %+v, %+v", run.Meta, run.Summary)
	}
	if run.Meta.SnapshotDir == "" {
		t.Fatal("snapshot attempt was not recorded")
	}
	if _, err := os.Stat(run.Meta.SnapshotDir); !os.IsNotExist(err) {
		t.Fatalf("canceled snapshot remains: %v", err)
	}
	recorded, err := events.ReadFile(store.RunFile(run.RunID, persistence.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) == 0 || recorded[len(recorded)-1].Type != events.RunFinished || recorded[len(recorded)-1].Status != string(engine.StepStateInterrupted) {
		t.Fatalf("missing interrupted run.finished: %+v", recorded)
	}
}
