package persistence

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Store struct {
	RepoRoot string
}

func NewStore(repoRoot string) Store {
	return Store{RepoRoot: repoRoot}
}

func (s Store) RunsRoot() string {
	return filepath.Join(s.RepoRoot, RunsDir)
}

func (s Store) RunDir(runID string) string {
	return filepath.Join(s.RunsRoot(), runID)
}

func (s Store) RunFile(runID string, name string) string {
	return filepath.Join(s.RunDir(runID), name)
}

func (s Store) StepDir(runID string, stepIndex int, stepID string) string {
	return filepath.Join(s.RunDir(runID), StepRelDir(stepIndex, stepID))
}

func (s Store) StepFile(runID string, stepIndex int, stepID string, name string) string {
	return filepath.Join(s.StepDir(runID, stepIndex, stepID), name)
}

func (s Store) EnsureRun(runID string) error {
	for _, dir := range []string{
		s.RunsRoot(),
		s.RunDir(runID),
		s.RunFile(runID, StepsDir),
	} {
		if err := EnsureDir(dir); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	return nil
}

func (s Store) EnsureStepDir(runID string, stepIndex int, stepID string) error {
	dir := s.StepDir(runID, stepIndex, stepID)
	if err := EnsureDir(dir); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	return nil
}

func (s Store) ListRunIDs() ([]string, error) {
	entries, err := os.ReadDir(s.RunsRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.RunsRoot(), err)
	}

	runIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runIDs = append(runIDs, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(runIDs)))
	return runIDs, nil
}
