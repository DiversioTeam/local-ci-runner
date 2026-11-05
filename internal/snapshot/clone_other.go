//go:build !darwin && !linux

package snapshot

import (
	"context"
	"fmt"
	"runtime"
)

func cloneTree(context.Context, string, string) error {
	return fmt.Errorf("copy-on-write seeding is not supported on %s", runtime.GOOS)
}

func plainCopy(context.Context, string, string) error {
	return fmt.Errorf("seeding is not supported on %s", runtime.GOOS)
}
