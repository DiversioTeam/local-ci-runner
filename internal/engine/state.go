package engine

import (
	"fmt"
	"os"
)

type StepState string

const (
	StepStatePending     StepState = "pending"
	StepStateRunning     StepState = "running"
	StepStateSuccess     StepState = "success"
	StepStateFailure     StepState = "failure"
	StepStateInterrupted StepState = "interrupted"
	StepStateSkipped     StepState = "skipped"
	StepStateBlocked     StepState = "blocked"
	StepStateStale       StepState = "stale"
)

func (s StepState) Terminal() bool {
	switch s {
	case StepStateSuccess, StepStateFailure, StepStateInterrupted, StepStateSkipped, StepStateBlocked, StepStateStale:
		return true
	default:
		return false
	}
}

// InterruptError carries the OS signal that canceled a run.
type InterruptError struct {
	Signal os.Signal
}

func (interruptError InterruptError) Error() string {
	if interruptError.Signal == nil {
		return "run interrupted"
	}
	return fmt.Sprintf("run interrupted by %s", interruptError.Signal)
}
