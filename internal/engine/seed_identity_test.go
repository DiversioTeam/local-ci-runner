package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/snapshot"
)

func TestChangedSeedIdentityRefusesExecution(t *testing.T) {
	t.Parallel()
	for _, externalLink := range []bool{false, true} {
		t.Run(fmt.Sprintf("external_link=%t", externalLink), func(t *testing.T) {
			plan := config.ResolvedPlan{Steps: []config.Step{{ID: "check", Command: []string{"sh", "-c", `touch "$LOCAL_CI_SOURCE_ROOT/.local-ci/executed"`}}}}
			root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
			writeFile(t, filepath.Join(root, ".git", "info", "exclude"), []byte(".env\n"))
			writeFile(t, filepath.Join(root, ".env"), []byte("before"))
			seedHash, err := snapshot.GetSeedHash(t.Context(), root, []string{".env"})
			if err != nil {
				t.Fatal(err)
			}
			if externalLink {
				externalPath := filepath.Join(t.TempDir(), "seed")
				writeFile(t, externalPath, []byte("after"))
				if err := os.Remove(filepath.Join(root, ".env")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalPath, filepath.Join(root, ".env")); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, filepath.Join(root, ".env"), []byte("after"))
			}
			_, err = ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{Seed: []string{".env"}, AllowPlainCopy: true, ExpectedSeedHash: seedHash}})
			if err == nil || !strings.Contains(err.Error(), "seed inputs changed") {
				t.Fatalf("expected changed-seed refusal: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".local-ci", "executed")); !os.IsNotExist(err) {
				t.Fatalf("step executed before validating copied inputs: %v", err)
			}
			if _, err := os.Stat(snapshot.Path(root, run.RunID)); !os.IsNotExist(err) {
				t.Fatalf("snapshot leaked after seed mismatch: %v", err)
			}
		})
	}
}

func TestUnverifiedInitialSeedsRemainUncacheableAfterValidCopy(t *testing.T) {
	t.Parallel()
	plan := config.ResolvedPlan{Steps: []config.Step{{ID: "check", Command: []string{"sh", "-c", `test "$(cat .env)" = ready`}}}}
	root, store, run := newSnapshotRun(t, plan, config.GitHub{}, "")
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), []byte(".env\n"))
	path := filepath.Join(root, ".env")
	external := filepath.Join(t.TempDir(), "seed")
	writeFile(t, external, []byte("ready"))
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	expected, err := snapshot.GetSeedHash(t.Context(), root, []string{".env"})
	if err == nil || expected != "" {
		t.Fatalf("initial external seed hash = %q, %v; want unverified inputs", expected, err)
	}
	// Recover before copying, deterministically reproducing an initially failed hash.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("ready"))
	if hash, err := snapshot.GetSeedHash(t.Context(), root, []string{".env"}); err != nil || hash == "" {
		t.Fatalf("recovered seed is not fingerprintable: %q, %v", hash, err)
	}
	executed, err := ExecuteRun(t.Context(), store, run, ExecuteOptions{Snapshot: &SnapshotOptions{
		Seed: []string{".env"}, AllowPlainCopy: true, ExpectedSeedHash: expected,
	}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRun(store, executed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary.Status != string(StepStateSuccess) || loaded.Meta.TestedTreeHash == "" {
		t.Fatalf("unverified initial inputs prevented fresh execution: %+v", loaded.Meta)
	}
	if loaded.Meta.SeedHash != "" {
		t.Fatalf("copied hash made initially unverified seeds reusable: %q", loaded.Meta.SeedHash)
	}
	requireSnapshotRemoved(t, loaded)
}
