// Package agentsession implements the opt-in `agent_session = true` check: a branch's HEAD must be covered by a
// published, redacted agent session transcript (the agent-session skill in DiversioTeam/agent-skills-marketplace),
// linked from the open PR's body, or the work must be declared agent-free with "No agent session: <reason>".
//
// This is Diversio-wide policy rather than one repository's workflow, so it lives in the runner as a built-in step that
// each repository opts into with one line. The step itself is an ordinary black-box command (`local-ci
// agent-session-check`) and adds no second execution path.
//
// The failure text is read by coding agents (Claude Code, Pi, Codex) as well as people: its first line starts with
// `agent-session: missing|stale|unlinked`, and the next lines give the exact command that fixes it. Tests pin it.
package agentsession

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// StepID and GitHubContext name the built-in step.
const (
	StepID        = "agent-session"
	StepName      = "Agent session transcript"
	GitHubContext = "local/agent-session"
	Subcommand    = "agent-session-check"
)

var (
	linkPattern        = regexp.MustCompile(`https://internal-share\.diversio\.com/(session-[A-Za-z0-9._%-]+\.html)`)
	coveredHeadPattern = regexp.MustCompile(`-h([0-9a-f]{12})-`)
	// The same declaration the skill writes and its hook accepts: a line with a reason of three or more characters.
	declarationPattern = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?No agent session:\s*\S.{2,}$`)
)

// PullRequest is the open PR for the branch, when there is one.
type PullRequest struct {
	Number int
	Body   string
}

// Published is one line of the skill's local record (~/.agent/sessions/published.jsonl).
type Published struct {
	Repo   string   `json:"repo"`
	Branch string   `json:"branch"`
	Head   string   `json:"head"`
	Links  []string `json:"links"`
	None   string   `json:"none"`
}

// Input is everything the check decides from; Check itself does no I/O.
type Input struct {
	RepoRoot  string
	RepoSlug  string
	Branch    string
	HeadSHA   string
	PR        *PullRequest
	Published *Published
	// The skill's CLI (agent_session.py), when it is installed; empty otherwise.
	CLI string
}

// Result is the outcome: OK, and the text to print.
type Result struct {
	OK      bool
	Kind    string
	Message string
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func coveredHeads(body string) []string {
	var heads []string
	for _, match := range linkPattern.FindAllStringSubmatch(body, -1) {
		if head := coveredHeadPattern.FindStringSubmatch(match[1]); head != nil {
			heads = append(heads, head[1])
		}
	}
	return heads
}

// Check decides whether HEAD is covered. With an open PR its body is authoritative; without one, the local record is.
func Check(in Input) Result {
	head := short(in.HeadSHA)
	where := fmt.Sprintf("for %s branch %s", in.RepoSlug, in.Branch)
	if in.PR != nil {
		where += fmt.Sprintf(" PR #%d", in.PR.Number)
	}
	if in.Branch == "" {
		return fail(in, "missing", "(detached HEAD; check out the PR's branch)", where)
	}
	publishedHere := in.Published != nil && in.Published.None == "" && strings.HasPrefix(in.Published.Head, head) && len(in.Published.Links) > 0

	if in.PR != nil {
		if declarationPattern.MatchString(in.PR.Body) {
			return Result{OK: true, Kind: "declared", Message: fmt.Sprintf("agent-session: ok (declared no agent session) %s", where)}
		}
		heads := coveredHeads(in.PR.Body)
		for _, covered := range heads {
			if covered == head {
				return Result{OK: true, Kind: "ok", Message: fmt.Sprintf("agent-session: ok (transcript covers HEAD %s) %s", head, where)}
			}
		}
		if publishedHere {
			return fail(in, "unlinked", fmt.Sprintf("(a transcript for HEAD %s is published, but the PR body does not link it)", head), where)
		}
		if len(heads) > 0 {
			return fail(in, "stale", fmt.Sprintf("(transcript covers %s, HEAD is %s)", heads[len(heads)-1], head), where)
		}
		return fail(in, "missing", "(the PR body has no agent session link)", where)
	}

	if in.Published != nil && in.Published.None != "" {
		return Result{OK: true, Kind: "declared", Message: fmt.Sprintf("agent-session: ok (declared no agent session) %s", where)}
	}
	if publishedHere {
		return Result{OK: true, Kind: "ok", Message: fmt.Sprintf("agent-session: ok (transcript covers HEAD %s; no PR yet) %s", head, where)}
	}
	if in.Published != nil && in.Published.Head != "" {
		return fail(in, "stale", fmt.Sprintf("(transcript covers %s, HEAD is %s)", short(in.Published.Head), head), where)
	}
	return fail(in, "missing", "(no transcript published for this branch)", where)
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func fail(in Input, kind string, detail string, where string) Result {
	cli := in.CLI
	install := ""
	if cli == "" {
		cli = "<agent-session skill>/scripts/agent_session.py"
		install = "install: claude plugin install agent-session@diversiotech (or the Codex/Pi agent-session skill), then run the fix\n"
	}
	share := fmt.Sprintf("python3 %s share --repo %s", quote(cli), quote(in.RepoRoot))
	none := fmt.Sprintf("python3 %s declare-none %s --repo %s", quote(cli), quote("<reason>"), quote(in.RepoRoot))
	if in.PR != nil {
		share += fmt.Sprintf(" --pr %d", in.PR.Number)
		none += fmt.Sprintf(" --pr %d", in.PR.Number)
	}
	message := fmt.Sprintf("agent-session: %s %s %s\n", kind, detail, where) +
		install +
		fmt.Sprintf("fix: %s\n", share) +
		"next: share renders a redacted preview, publishes it to internal-share, and puts its link in the PR body's \"## Agent sessions\" section; " +
		"on a branch's first publish it stops (exit 3) for the developer's review, then rerun with --yes. Run local-ci again afterwards.\n" +
		fmt.Sprintf("no agent worked on this branch: %s (writes \"No agent session: <reason>\")", none)
	return Result{OK: false, Kind: kind, Message: message}
}

// StorePath is the skill's local record, honouring AGENT_SESSION_HOME as the skill does.
func StorePath(home string) string {
	if base := os.Getenv("AGENT_SESSION_HOME"); base != "" {
		return filepath.Join(base, "published.jsonl")
	}
	return filepath.Join(home, ".agent", "sessions", "published.jsonl")
}

// LatestPublished returns the newest record for the repository and branch, or nil.
func LatestPublished(path string, repo string, branch string) (*Published, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var latest *Published
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var entry Published
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Repo == repo && entry.Branch == branch {
			copied := entry
			latest = &copied
		}
	}
	return latest, scanner.Err()
}

// FindCLI looks for the skill's agent_session.py where Claude Code, Codex, and Pi install skills.
func FindCLI(home string) string {
	if explicit := os.Getenv("AGENT_SESSION_CLI"); explicit != "" {
		return explicit
	}
	const tail = "skills/agent-session/scripts/agent_session.py"
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	candidates, _ := filepath.Glob(filepath.Join(home, ".claude", "plugins", "cache", "*", "agent-session", "*", tail))
	// The newest installed plugin version wins.
	sort.Slice(candidates, func(i, j int) bool { return modTime(candidates[i]) > modTime(candidates[j]) })
	candidates = append(candidates,
		filepath.Join(codexHome, "skills", "agent-session", "scripts", "agent_session.py"),
		filepath.Join(home, ".pi", "agent", tail),
		filepath.Join(home, ".agents", tail),
	)
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func modTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}
