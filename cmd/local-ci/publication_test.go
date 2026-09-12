package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/engine"
	"github.com/DiversioTeam/local-ci-runner/internal/events"
	ghstatus "github.com/DiversioTeam/local-ci-runner/internal/github"
)

func TestBuiltBinaryDocumentsReceiptsWithoutSourceOrGit(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "local-ci")
	command := exec.Command("go", "build", "-ldflags", "-X github.com/DiversioTeam/local-ci-runner/internal/update.Version=v9.8.7-test", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	outsideRepo := t.TempDir()
	for _, args := range [][]string{{"--help"}, {"manual"}, {"help", "all"}, {"help", "show"}, {"publish", "--help"}, {"version"}} {
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
		for _, text := range []string{fmt.Sprintf("github_post.version = %d", events.PublicationVersion), "github.status.requested", "github.status.posted", "github.status.failed", "unknown", "NOT a dry", "--runner --json"} {
			if !strings.Contains(manual, text) {
				t.Fatalf("binary manual missing %q", text)
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

type successfulPublicationReporter struct{}

func (successfulPublicationReporter) PostStatus(context.Context, ghstatus.Target, ghstatus.Status) error {
	return nil
}

func TestRunnerLogsExposePublicationWithoutWritingArtifacts(t *testing.T) {
	fixture := newCLIFixture(t)
	run := fixture.finishedRun
	run.Meta.GitHubEnabled = true
	run.Meta.GitHubPostingSuppressed = "cli_disabled"
	if err := engine.PublishCompletedRun(t.Context(), fixture.store, run, engine.PublishOptions{TargetSHA: run.Meta.HeadSHA, Reporter: successfulPublicationReporter{}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run.RunDir, "events.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := newCLI(&stdout, &stderr, fixture.root).run([]string{"logs", run.RunID, "--runner", "--json"}); err != nil {
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
	if posted != 4 {
		t.Fatalf("posted=%d", posted)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("inspection changed event log")
	}
}
