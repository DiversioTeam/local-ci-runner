package github

type State string

const (
	StatePending State = "pending"
	StateSuccess State = "success"
	StateFailure State = "failure"
)

type Target struct {
	Repo string `json:"repo"`
	SHA  string `json:"sha"`
}

type Status struct {
	Context     string `json:"context"`
	State       State  `json:"state"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}
