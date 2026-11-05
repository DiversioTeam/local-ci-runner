package config

const (
	DefaultPath             = ".local-ci.toml"
	DefaultAggregateContext = "local/verify"
	DefaultWorkingDir       = "."
)

type File struct {
	Version     int      `toml:"version" json:"version"`
	MaxParallel int      `toml:"max_parallel" json:"max_parallel,omitempty"`
	Planner     *Planner `toml:"planner" json:"planner,omitempty"`
	GitHub      GitHub   `toml:"github" json:"github"`
	Snapshot    Snapshot `toml:"snapshot" json:"snapshot"`
	Steps       []Step   `toml:"steps" json:"steps,omitempty"`
}

// Snapshot controls the immutable checkout that steps run in.
//
// Steps run in a detached Git worktree of the exact tree being verified, so an
// edit, checkout, or commit in the live worktree during a run cannot change
// what is tested. Seed lists Git-ignored paths (virtualenvs, node_modules,
// build caches, env files) copied into the snapshot with copy-on-write clones.
type Snapshot struct {
	// Disabled runs steps in the live worktree, as before snapshots existed.
	// Such runs record no tested tree and post with the old, weaker guarantee.
	Disabled bool `toml:"disabled" json:"disabled,omitempty"`
	// Seed are repo-relative, Git-ignored paths cloned into each snapshot.
	Seed []string `toml:"seed" json:"seed,omitempty"`
	// AllowPlainCopy permits a full copy when the filesystem cannot clone.
	AllowPlainCopy bool `toml:"allow_plain_copy" json:"allow_plain_copy,omitempty"`
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
	Timeout       string            `toml:"timeout" json:"timeout,omitempty"`
	GitHubContext string            `toml:"github_context" json:"github_context,omitempty"`
	Env           map[string]string `toml:"env" json:"env,omitempty"`
}

type ResolvedPlan struct {
	Env   map[string]string `json:"env,omitempty"`
	Steps []Step            `json:"steps"`
}

func (f *File) applyDefaults() {
	if f.MaxParallel == 0 {
		f.MaxParallel = 1
	}
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

// Clone returns a deep copy, so callers can apply defaults without touching the original.
func (p ResolvedPlan) Clone() ResolvedPlan {
	return ResolvedPlan{Env: cloneStringMap(p.Env), Steps: cloneSteps(p.Steps)}
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
		Timeout:       step.Timeout,
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
