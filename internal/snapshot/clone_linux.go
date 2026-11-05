package snapshot

import "context"

// cloneTree clones source to target with reflinks. --reflink=always fails
// instead of silently copying when the filesystem cannot share extents.
func cloneTree(ctx context.Context, source string, target string) error {
	_, err := run(ctx, "", "cp", "-a", "--reflink=always", source, target)
	return err
}

func plainCopy(ctx context.Context, source string, target string) error {
	_, err := run(ctx, "", "cp", "-a", source, target)
	return err
}
