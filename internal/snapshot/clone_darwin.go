package snapshot

import (
	"context"
	"fmt"

	"golang.org/x/sys/unix"
)

// cloneTree clones source to target with one clonefile(2) call, which on APFS
// clones a whole directory hierarchy copy-on-write. It fails, rather than
// copying, when the filesystem or volume cannot clone.
func cloneTree(_ context.Context, source string, target string) error {
	if err := unix.Clonefile(source, target, unix.CLONE_NOFOLLOW); err != nil {
		return fmt.Errorf("clonefile %s: %w", source, err)
	}
	return nil
}

func plainCopy(ctx context.Context, source string, target string) error {
	_, err := run(ctx, "", "cp", "-R", "-p", source, target)
	return err
}
