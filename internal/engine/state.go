package engine

type StepState string

const (
	StepStatePending StepState = "pending"
	StepStateRunning StepState = "running"
	StepStateSuccess StepState = "success"
	StepStateFailure StepState = "failure"
	StepStateSkipped StepState = "skipped"
	StepStateBlocked StepState = "blocked"
	StepStateStale   StepState = "stale"
)

func (s StepState) Terminal() bool {
	switch s {
	case StepStateSuccess, StepStateFailure, StepStateSkipped, StepStateBlocked, StepStateStale:
		return true
	default:
		return false
	}
}
