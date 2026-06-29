package config

const (
	DefaultPath             = ".local-ci.toml"
	DefaultAggregateContext = "local/verify"
	DefaultWorkingDir       = "."
)

type File struct {
	Version int      `toml:"version" json:"version"`
	Planner *Planner `toml:"planner" json:"planner,omitempty"`
	GitHub  GitHub   `toml:"github" json:"github"`
	Steps   []Step   `toml:"steps" json:"steps,omitempty"`
}

type Planner struct {
	Command []string          `toml:"command" json:"command"`
	Dir     string            `toml:"dir" json:"dir,omitempty"`
	Env     map[string]string `toml:"env" json:"env,omitempty"`
}

type GitHub struct {
	Enabled          bool   `toml:"enabled" json:"enabled"`
	AggregateContext string `toml:"aggregate_context" json:"aggregate_context,omitempty"`
}

type Step struct {
	ID            string            `toml:"id" json:"id"`
	Name          string            `toml:"name" json:"name,omitempty"`
	Command       []string          `toml:"command" json:"command"`
	Dir           string            `toml:"dir" json:"dir,omitempty"`
	Needs         []string          `toml:"needs" json:"needs,omitempty"`
	If            string            `toml:"if" json:"if,omitempty"`
	GitHubContext string            `toml:"github_context" json:"github_context,omitempty"`
	Env           map[string]string `toml:"env" json:"env,omitempty"`
}

type ResolvedPlan struct {
	Env   map[string]string `json:"env,omitempty"`
	Steps []Step            `json:"steps"`
}

func (f *File) ApplyDefaults() {
	if f.GitHub.AggregateContext == "" {
		f.GitHub.AggregateContext = DefaultAggregateContext
	}

	applyStepDefaults(f.Steps)

	if f.Planner != nil && f.Planner.Dir == "" {
		f.Planner.Dir = DefaultWorkingDir
	}
}

func (f File) StaticPlan() ResolvedPlan {
	plan := ResolvedPlan{Steps: cloneSteps(f.Steps)}
	plan.ApplyDefaults()
	return plan
}

func (p *ResolvedPlan) ApplyDefaults() {
	applyStepDefaults(p.Steps)
}

func (p ResolvedPlan) Validate() error {
	return validateSteps(p.Steps, false)
}

func (s Step) EffectiveGitHubContext() string {
	if s.GitHubContext != "" {
		return s.GitHubContext
	}
	return "local/" + s.ID
}

func applyStepDefaults(steps []Step) {
	for i := range steps {
		if steps[i].Name == "" {
			steps[i].Name = steps[i].ID
		}
		if steps[i].Dir == "" {
			steps[i].Dir = DefaultWorkingDir
		}
		if steps[i].GitHubContext == "" && steps[i].ID != "" {
			steps[i].GitHubContext = steps[i].EffectiveGitHubContext()
		}
	}
}

func cloneSteps(src []Step) []Step {
	if src == nil {
		return nil
	}

	dst := make([]Step, len(src))
	for i, step := range src {
		dst[i] = cloneStep(step)
	}
	return dst
}

func cloneStep(step Step) Step {
	return Step{
		ID:            step.ID,
		Name:          step.Name,
		Command:       cloneStrings(step.Command),
		Dir:           step.Dir,
		Needs:         cloneStrings(step.Needs),
		If:            step.If,
		GitHubContext: step.GitHubContext,
		Env:           cloneStringMap(step.Env),
	}
}

func cloneStrings(src []string) []string {
	return append([]string(nil), src...)
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}

	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
