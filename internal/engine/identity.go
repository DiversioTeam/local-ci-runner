package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DiversioTeam/local-ci-runner/internal/config"
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

type RunIdentity struct {
	RepoRoot         string
	RepoSlug         string
	HeadSHA          string
	ConfigPath       string
	ConfigHash       string
	PlanHash         string
	WorktreeTreeHash string
}

func BuildRunIdentity(repoRoot string, repoSlug string, headSHA string, configPath string, plan config.ResolvedPlan) (RunIdentity, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return RunIdentity{}, fmt.Errorf("repo root is required")
	}
	if strings.TrimSpace(configPath) == "" {
		return RunIdentity{}, fmt.Errorf("config path is required")
	}

	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return RunIdentity{}, fmt.Errorf("resolve repo root: %w", err)
	}

	resolvedConfigPath := configPath
	if !filepath.IsAbs(resolvedConfigPath) {
		resolvedConfigPath = filepath.Join(absRepoRoot, resolvedConfigPath)
	}
	absConfigPath, err := filepath.Abs(resolvedConfigPath)
	if err != nil {
		return RunIdentity{}, fmt.Errorf("resolve config path: %w", err)
	}

	configHash, err := HashFile(absConfigPath)
	if err != nil {
		return RunIdentity{}, err
	}
	planHash, err := HashPlan(plan)
	if err != nil {
		return RunIdentity{}, err
	}

	identity := RunIdentity{
		RepoRoot:   absRepoRoot,
		RepoSlug:   repoSlug,
		HeadSHA:    headSHA,
		ConfigPath: absConfigPath,
		ConfigHash: configHash,
		PlanHash:   planHash,
	}
	if err := identity.Validate(); err != nil {
		return RunIdentity{}, err
	}

	return identity, nil
}

func (identity RunIdentity) Validate() error {
	for _, field := range []struct {
		name     string
		value    string
		required bool
	}{
		{name: "repo root", value: identity.RepoRoot, required: true},
		{name: "repo slug", value: identity.RepoSlug, required: true},
		{name: "HEAD SHA", value: identity.HeadSHA, required: true},
		{name: "config path", value: identity.ConfigPath, required: true},
		{name: "config hash", value: identity.ConfigHash, required: true},
		{name: "plan hash", value: identity.PlanHash, required: true},
		{name: "worktree tree hash", value: identity.WorktreeTreeHash, required: false},
	} {
		if !field.required {
			continue
		}
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}

	return nil
}

func ValidateResume(meta persistence.Meta, identity RunIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}

	for _, field := range []struct {
		name    string
		stored  string
		current string
	}{
		{name: "repo root", stored: meta.RepoRoot, current: identity.RepoRoot},
		{name: "repo slug", stored: meta.RepoSlug, current: identity.RepoSlug},
		{name: "HEAD SHA", stored: meta.HeadSHA, current: identity.HeadSHA},
		{name: "config path", stored: meta.ConfigPath, current: identity.ConfigPath},
		{name: "config hash", stored: meta.ConfigHash, current: identity.ConfigHash},
		{name: "plan hash", stored: meta.PlanHash, current: identity.PlanHash},
	} {
		if field.stored != field.current {
			return fmt.Errorf("resume refused: %s changed", field.name)
		}
	}
	if strings.TrimSpace(meta.WorktreeTreeHash) != "" && strings.TrimSpace(identity.WorktreeTreeHash) != "" && meta.WorktreeTreeHash != identity.WorktreeTreeHash {
		return fmt.Errorf("resume refused: worktree tree hash changed (stored %s, current %s)", meta.WorktreeTreeHash, identity.WorktreeTreeHash)
	}
	return nil
}

func HashFile(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	return HashBytes(payload), nil
}

func HashPlan(plan config.ResolvedPlan) (string, error) {
	copyPlan := config.ResolvedPlan{
		Env:   cloneStringMap(plan.Env),
		Steps: cloneSteps(plan.Steps),
	}
	copyPlan.ApplyDefaults()
	if err := copyPlan.Validate(); err != nil {
		return "", fmt.Errorf("validate plan for hashing: %w", err)
	}

	payload, err := json.Marshal(copyPlan)
	if err != nil {
		return "", fmt.Errorf("marshal plan for hashing: %w", err)
	}

	return HashBytes(payload), nil
}

func HashBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
