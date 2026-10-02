package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func TestBuiltBinaryDocumentsReceiptsWithoutSourceOrGit(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "local-ci")
	command := exec.Command("go", "build", "-ldflags", "-X github.com/DiversioTeam/local-ci-runner/internal/update.Version=v9.8.7-test", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	outsideRepo := t.TempDir()
	for _, args := range [][]string{{"--help"}, {"manual"}, {"help", "all"}, {"help", "show"}, {"help", "plan"}, {"run", "--help"}, {"resume", "--help"}, {"publish", "--help"}, {"version"}} {
		command := exec.Command(binary, args...)
		command.Dir = outsideRepo
		command.Env = []string{"PATH=/no-external-programs", "HOME=" + outsideRepo}
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, output)
		}
		if !bytes.HasPrefix(output, []byte("local-ci v9.8.7-test\n")) {
			t.Fatalf("help is not version-matched: %s", output)
		}
		if args[0] != "manual" {
			continue
		}
		manual := string(output)
		for _, text := range []string{fmt.Sprintf("github_post.version = %d", events.PublicationVersion), "github.status.requested", "github.status.posted", "github.status.failed", "NOT a dry", "--runner --json"} {
			if !strings.Contains(manual, text) {
				t.Fatalf("binary manual missing %q", text)
			}
		}
		// Persisted field names are checked by reflection below; these are the operator-facing names.
		for _, text := range []string{"--max-parallel", "--from-step", "local-ci plan", "run --json"} {
			if !strings.Contains(manual, text) {
				t.Fatalf("binary manual missing execution contract %q", text)
			}
		}
		for _, contract := range []reflect.Type{reflect.TypeFor[persistence.Meta](), reflect.TypeFor[persistence.StepStatus]()} {
			for index := 0; index < contract.NumField(); index++ {
				name := strings.Split(contract.Field(index).Tag.Get("json"), ",")[0]
				if !strings.Contains(manual, "`"+name+"`") {
					t.Fatalf("undocumented persisted execution field %s", name)
				}
			}
		}
		contract := reflect.TypeFor[events.GitHubPost]()
		for index := 0; index < contract.NumField(); index++ {
			name := strings.Split(contract.Field(index).Tag.Get("json"), ",")[0]
			if !strings.Contains(manual, "`"+name+"`") {
				t.Fatalf("undocumented receipt field %s", name)
			}
		}
	}
}

func TestBuiltBinaryExitStatus(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "local-ci")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	failingRepo := newGitRepo(t)
	writeFile(t, filepath.Join(failingRepo, config.DefaultPath), []byte("version = 1\n[[steps]]\nid = 'fail'\ncommand = ['sh', '-c', 'exit 7']\n"))
	// The manual documents 1 for an unsuccessful local run and 2 for usage errors.
	for _, test := range []struct {
		name     string
		dir      string
		args     []string
		wantCode int
	}{
		{name: "unsuccessful run", dir: failingRepo, args: []string{"run", "--no-github"}, wantCode: 1},
		{name: "usage error", dir: t.TempDir(), args: []string{"bogus"}, wantCode: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(binary, test.args...)
			command.Dir = test.dir
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != test.wantCode {
				t.Fatalf("exit = %v, want status %d\n%s", err, test.wantCode, output)
			}
		})
	}
}

type successfulPublicationReporter struct{}

func (successfulPublicationReporter) PostStatus(context.Context, ghstatus.Target, ghstatus.Status) error {
	return nil
}

func TestRunnerLogsExposePublicationWithoutWritingArtifacts(t *testing.T) {
	root := newGitRepo(t)
	writeFile(t, filepath.Join(root, config.DefaultPath), []byte("version = 1\n[github]\nenabled = true\n[[steps]]\nid = 'check'\ncommand = ['sh', '-c', 'exit 0']\n"))
	var stdout bytes.Buffer
	// --no-github records why posting was suppressed, which is what makes the run publishable.
	if err := newCLI(&stdout, &bytes.Buffer{}, root).runWithContext(t.Context(), nil, []string{"run", "--no-github", "--json"}); err != nil {
		t.Fatal(err)
	}
	var completed showJSON
	if err := json.Unmarshal(stdout.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	store := persistence.NewStore(root)
	run, err := engine.LoadRun(store, completed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.PublishCompletedRun(t.Context(), store, run, engine.PublishOptions{TargetSHA: run.Meta.HeadSHA, Reporter: successfulPublicationReporter{}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run.RunDir, "events.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	stdout.Reset()
	if err := newCLI(&stdout, &stderr, root).runWithContext(t.Context(), nil, []string{"logs", run.RunID, "--runner", "--json"}); err != nil {
		t.Fatal(err)
	}
	var output logsJSON
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	posted := 0
	for _, event := range output.Events {
		if event.Type != events.GitHubStatusPosted {
			continue
		}
		if event.GitHubPost == nil || event.GitHubPost.SHA != run.Meta.HeadSHA || event.GitHubPost.Source != events.PublicationPublish {
			t.Fatal("missing publication target")
		}
		posted++
	}
	// How many contexts publication posts is the engine's contract; this test owns exposure.
	if posted == 0 {
		t.Fatal("logs --runner --json exposed no publication receipts")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("inspection changed event log")
	}
}
