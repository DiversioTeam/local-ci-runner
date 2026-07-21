//go:build darwin || linux

package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

const processStopGracePeriod = 2 * time.Second

func addProcessCancellation(commandContext context.Context, command *exec.Cmd, forceStop <-chan struct{}) func() {
	// Separate groups let cancellation reach descendants without touching sibling steps.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = processStopGracePeriod + time.Second

	commandDone := make(chan struct{})
	var cancellationStarted atomic.Bool
	command.Cancel = func() error {
		signalError := addSignalToProcessGroup(command, getInterruptSignal(commandContext))
		if !errors.Is(signalError, os.ErrProcessDone) {
			cancellationStarted.Store(true)
			go removeProcessGroupAfterGrace(command, forceStop, commandDone)
		}
		return signalError
	}

	return func() {
		close(commandDone)
		if cancellationStarted.Load() {
			// The group may already be gone; shutdown cannot recover from a kill error.
			_ = addSignalToProcessGroup(command, syscall.SIGKILL)
		}
	}
}

func getInterruptSignal(commandContext context.Context) syscall.Signal {
	var interruptError InterruptError
	if errors.As(context.Cause(commandContext), &interruptError) {
		if interruptSignal, ok := interruptError.Signal.(syscall.Signal); ok {
			return interruptSignal
		}
	}
	return syscall.SIGTERM
}

func removeProcessGroupAfterGrace(command *exec.Cmd, forceStop <-chan struct{}, commandDone <-chan struct{}) {
	select {
	case <-commandDone:
		return
	default:
	}

	timer := time.NewTimer(processStopGracePeriod)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-forceStop:
	case <-commandDone:
		return
	}
	// The group may already be gone; shutdown cannot recover from a kill error.
	_ = addSignalToProcessGroup(command, syscall.SIGKILL)
}

func addSignalToProcessGroup(command *exec.Cmd, processSignal syscall.Signal) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-command.Process.Pid, processSignal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
