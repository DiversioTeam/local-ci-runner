package engine

import (
	"github.com/DiversioTeam/local-ci-runner/internal/persistence"
)

func cloneStrings(src []string) []string {
	return append([]string(nil), src...)
}

func cloneWorktreeFiles(src []persistence.WorktreeFile) []persistence.WorktreeFile {
	if src == nil {
		return nil
	}
	dst := make([]persistence.WorktreeFile, len(src))
	copy(dst, src)
	return dst
}
