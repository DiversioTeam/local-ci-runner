//go:build !darwin && !linux

package engine

import (
	"context"
	"os/exec"
	"time"
)

const processStopGracePeriod = 2 * time.Second

func addProcessCancellation(_ context.Context, command *exec.Cmd, _ <-chan struct{}) func() {
	command.WaitDelay = processStopGracePeriod
	return func() {}
}
