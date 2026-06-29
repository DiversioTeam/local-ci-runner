package persistence

import (
	"fmt"
	"path/filepath"
)

const (
	RunsDir        = ".local-ci/runs"
	MetaFile       = "meta.json"
	PlanFile       = "plan.json"
	PlanEnvFile    = "plan.env"
	SummaryFile    = "summary.json"
	SummaryText    = "summary.txt"
	EventsFile     = "events.jsonl"
	PlannerLogFile = "planner.log"
	StepsDir       = "steps"
	StatusFile     = "status.json"
	StdoutLog      = "stdout.log"
	StderrLog      = "stderr.log"
	CombinedLog    = "combined.log"
	OutputEnv      = "output.env"
)

func StepDirName(index int, stepID string) string {
	return fmt.Sprintf("%03d-%s", index+1, stepID)
}

func StepRelDir(index int, stepID string) string {
	return filepath.Join(StepsDir, StepDirName(index, stepID))
}

func StepRelPath(index int, stepID string, name string) string {
	return filepath.Join(StepRelDir(index, stepID), name)
}
