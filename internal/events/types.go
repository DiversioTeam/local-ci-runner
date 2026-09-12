package events

import "time"

type Type string

const (
	RunStarted            Type = "run.started"
	RunFinished           Type = "run.finished"
	StepStarted           Type = "step.started"
	StepFinished          Type = "step.finished"
	StepSkipped           Type = "step.skipped"
	StepBlocked           Type = "step.blocked"
	StepStale             Type = "step.stale"
	GitHubStatusRequested Type = "github.status.requested"
	GitHubStatusPosted    Type = "github.status.posted"
	GitHubStatusFailed    Type = "github.status.failed"
)

const PublicationVersion = 1

type PublicationSource string

const (
	PublicationExecution PublicationSource = "execution"
	PublicationPublish   PublicationSource = "publish"
)

// GitHubPost identifies one request, not a guess based on a context name or time.
type GitHubPost struct {
	Version   int               `json:"version"`
	AttemptID string            `json:"attempt_id"`
	Repo      string            `json:"repo"`
	SHA       string            `json:"sha"`
	Context   string            `json:"context"`
	Source    PublicationSource `json:"source"`
}

type Event struct {
	GitHubPost *GitHubPost `json:"github_post,omitempty"`
	Sequence   int64       `json:"sequence"`
	Time       time.Time   `json:"time"`
	RunID      string      `json:"run_id"`
	Type       Type        `json:"type"`
	StepID     string      `json:"step_id,omitempty"`
	Status     string      `json:"status,omitempty"`
	Message    string      `json:"message,omitempty"`
}
