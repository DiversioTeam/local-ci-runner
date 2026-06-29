package events

import "time"

type Type string

const (
	RunStarted         Type = "run.started"
	RunFinished        Type = "run.finished"
	StepStarted        Type = "step.started"
	StepFinished       Type = "step.finished"
	StepSkipped        Type = "step.skipped"
	StepBlocked        Type = "step.blocked"
	StepStale          Type = "step.stale"
	GitHubStatusPosted Type = "github.status.posted"
	GitHubStatusFailed Type = "github.status.failed"
)

type Event struct {
	Sequence int64     `json:"sequence"`
	Time     time.Time `json:"time"`
	RunID    string    `json:"run_id"`
	Type     Type      `json:"type"`
	StepID   string    `json:"step_id,omitempty"`
	Status   string    `json:"status,omitempty"`
	Message  string    `json:"message,omitempty"`
}
