package agentsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	head    = "0123456789abcdef0123456789abcdef01234567"
	oldHead = "fedcba9876543210fedcba9876543210fedcba98"
	cli     = "/home/dev/.claude/plugins/cache/diversiotech/agent-session/0.2.0/skills/agent-session/scripts/agent_session.py"
	link    = "https://internal-share.diversio.com/session-courage-feature-x-20261023-h0123456789ab-a1b2c3.html"
	oldLink = "https://internal-share.diversio.com/session-courage-feature-x-20261022-hfedcba987654-d4e5f6.html"
)

func input() Input {
	return Input{RepoRoot: "/work/courage", RepoSlug: "DiversioTeam/courage", Branch: "feature/x", HeadSHA: head, CLI: cli}
}

// The failure text is a contract: coding agents grep its first line and run its fix line. Change it deliberately.
func TestFailureOutputIsPinned(t *testing.T) {
	in := input()
	in.PR = &PullRequest{Number: 79, Body: "## Agent sessions\n\n- [Agent session transcript](" + oldLink + ")\n"}
	got := Check(in)
	want := `agent-session: stale (transcript covers fedcba987654, HEAD is 0123456789ab) for DiversioTeam/courage branch feature/x PR #79
fix: python3 '` + cli + `' share --repo '/work/courage' --pr 79
next: share renders a redacted preview, publishes it to internal-share, and puts its link in the PR body's "## Agent sessions" section; on a branch's first publish it stops (exit 3) for the developer's review, then rerun with --yes. Run local-ci again afterwards.
no agent worked on this branch: python3 '` + cli + `' declare-none '<reason>' --repo '/work/courage' --pr 79 (writes "No agent session: <reason>")`
	if got.OK || got.Kind != "stale" || got.Message != want {
		t.Fatalf("got %+v\nwant message:\n%s", got, want)
	}
}

func TestMissingWithoutCLIExplainsInstall(t *testing.T) {
	in := input()
	in.CLI = ""
	got := Check(in)
	want := `agent-session: missing (no transcript published for this branch) for DiversioTeam/courage branch feature/x
install: claude plugin install agent-session@diversiotech (or the Codex/Pi agent-session skill), then run the fix
fix: python3 '<agent-session skill>/scripts/agent_session.py' share --repo '/work/courage'
next: share renders a redacted preview, publishes it to internal-share, and puts its link in the PR body's "## Agent sessions" section; on a branch's first publish it stops (exit 3) for the developer's review, then rerun with --yes. Run local-ci again afterwards.
no agent worked on this branch: python3 '<agent-session skill>/scripts/agent_session.py' declare-none '<reason>' --repo '/work/courage' (writes "No agent session: <reason>")`
	if got.OK || got.Message != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got.Message, want)
	}
}

func TestPathsAreQuotedLiterally(t *testing.T) {
	in := input()
	in.RepoRoot = "/work/$(touch marker)/it's"
	got := Check(in)
	if !strings.Contains(got.Message, `--repo '/work/$(touch marker)/it'\''s'`) {
		t.Fatalf("unsafe quoting:\n%s", got.Message)
	}
}

func TestLookupFailureFailsClosed(t *testing.T) {
	in := input()
	in.Published = &Published{Repo: "DiversioTeam/courage", Branch: "feature/x", Head: head, Links: []string{link}}
	in.PRLookupError = "gh could not read the PR (check gh auth status or LOCAL_CI_GITHUB_TOKEN)"
	got := Check(in)
	want := "agent-session: unverified (gh could not read the PR (check gh auth status or LOCAL_CI_GITHUB_TOKEN)) for DiversioTeam/courage branch feature/x\n" +
		"fix: gh auth login (or set LOCAL_CI_GITHUB_TOKEN), then run local-ci again\n" +
		"next: with the PR readable, the check compares the PR body's agent session link with HEAD"
	if got.OK || got.Kind != "unverified" || got.Message != want {
		t.Fatalf("got %+v", got)
	}
}

func TestDecisions(t *testing.T) {
	current := &Published{Repo: "DiversioTeam/courage", Branch: "feature/x", Head: head, Links: []string{link}}
	old := &Published{Repo: "DiversioTeam/courage", Branch: "feature/x", Head: oldHead, Links: []string{oldLink}}
	cases := []struct {
		name      string
		pr        *PullRequest
		published *Published
		branch    string
		ok        bool
		kind      string
	}{
		{"PR links HEAD", &PullRequest{Number: 1, Body: "x\n" + link}, nil, "feature/x", true, "ok"},
		{"PR links HEAD among parts and old links", &PullRequest{Number: 1, Body: oldLink + "\n" + link}, nil, "feature/x", true, "ok"},
		{"PR declares no agent", &PullRequest{Number: 1, Body: "## Agent sessions\n\nNo agent session: hand-edited config"}, nil, "feature/x", true, "declared"},
		{"declaration needs a reason", &PullRequest{Number: 1, Body: "No agent session:"}, nil, "feature/x", false, "missing"},
		{"PR links an old head", &PullRequest{Number: 1, Body: oldLink}, nil, "feature/x", false, "stale"},
		{"published for HEAD but PR not updated", &PullRequest{Number: 1, Body: oldLink}, current, "feature/x", false, "unlinked"},
		{"PR without a link", &PullRequest{Number: 1, Body: "Summary"}, nil, "feature/x", false, "missing"},
		{"a link to an unrelated page is not a transcript", &PullRequest{Number: 1, Body: "https://internal-share.diversio.com/report.html"}, nil, "feature/x", false, "missing"},
		{"no PR, published for HEAD", nil, current, "feature/x", true, "ok"},
		{"no PR, published for an old head", nil, old, "feature/x", false, "stale"},
		{"no PR, declared", nil, &Published{Repo: "DiversioTeam/courage", Branch: "feature/x", Head: oldHead, None: "manual fix"}, "feature/x", true, "declared"},
		{"no PR, nothing", nil, nil, "feature/x", false, "missing"},
		{"detached HEAD", nil, current, "", false, "missing"},
	}
	for _, tc := range cases {
		in := input()
		in.PR, in.Published, in.Branch = tc.pr, tc.published, tc.branch
		got := Check(in)
		if got.OK != tc.ok || got.Kind != tc.kind {
			t.Errorf("%s: got ok=%v kind=%q\n%s", tc.name, got.OK, got.Kind, got.Message)
		}
	}
}

func TestLatestPublishedReadsTheSkillsRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "published.jsonl")
	lines := `{"repo": "DiversioTeam/courage", "branch": "feature/x", "head": "` + oldHead + `", "links": ["a"]}
not json
{"repo": "DiversioTeam/other", "branch": "feature/x", "head": "` + head + `", "links": ["b"]}
{"repo": "DiversioTeam/courage", "branch": "feature/x", "head": "` + head + `", "links": ["c"], "names": ["n"], "pr": 79}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LatestPublished(path, "DiversioTeam/courage", "feature/x")
	if err != nil || got == nil || got.Head != head || got.Links[0] != "c" {
		t.Fatalf("got %+v, %v", got, err)
	}
	missing, err := LatestPublished(filepath.Join(t.TempDir(), "none.jsonl"), "DiversioTeam/courage", "feature/x")
	if err != nil || missing != nil {
		t.Fatalf("missing file: %+v, %v", missing, err)
	}
}

func TestFindCLIPrefersExplicitThenInstalledSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AGENT_SESSION_CLI", "")
	t.Setenv("CODEX_HOME", "")
	if got := FindCLI(home); got != "" {
		t.Fatalf("nothing installed: %q", got)
	}
	codex := filepath.Join(home, ".codex", "skills", "agent-session", "scripts", "agent_session.py")
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindCLI(home); got != codex {
		t.Fatalf("codex skill: %q", got)
	}
	claude := filepath.Join(home, ".claude", "plugins", "cache", "diversiotech", "agent-session", "0.2.0", "skills", "agent-session", "scripts", "agent_session.py")
	if err := os.MkdirAll(filepath.Dir(claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindCLI(home); got != claude {
		t.Fatalf("claude plugin: %q", got)
	}
	t.Setenv("AGENT_SESSION_CLI", "/explicit/agent_session.py")
	if got := FindCLI(home); got != "/explicit/agent_session.py" {
		t.Fatalf("explicit: %q", got)
	}
}
