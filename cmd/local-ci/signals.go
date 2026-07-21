package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/DiversioTeam/local-ci-runner/internal/engine"
)

// getTerminationControl returns graceful and forced stop signals for one command.
// The caller must invoke the returned cleanup function before exiting.
func getTerminationControl(parentContext context.Context) (context.Context, <-chan struct{}, func()) {
	terminationSignals := make(chan os.Signal, 2)
	signal.Notify(terminationSignals, os.Interrupt, syscall.SIGTERM)
	return buildTerminationControl(parentContext, terminationSignals, func() {
		signal.Stop(terminationSignals)
	})
}

func buildTerminationControl(
	parentContext context.Context,
	terminationSignals <-chan os.Signal,
	removeSignalNotifications func(),
) (context.Context, <-chan struct{}, func()) {
	commandContext, cancelCommand := context.WithCancelCause(parentContext)
	forceStop := make(chan struct{})
	notificationsRemoved := make(chan struct{})
	notificationsDone := make(chan struct{})

	var removeSignalOnce sync.Once
	removeSignals := func() {
		removeSignalOnce.Do(removeSignalNotifications)
	}

	// The first signal cancels all work; the second forces processes and restores default handling.
	go func() {
		defer close(notificationsDone)

		select {
		case receivedSignal := <-terminationSignals:
			cancelCommand(engine.InterruptError{Signal: receivedSignal})
		case <-parentContext.Done():
			cancelCommand(context.Cause(parentContext))
			return
		case <-notificationsRemoved:
			return
		}

		select {
		case <-terminationSignals:
			close(forceStop)
			removeSignals()
		case <-notificationsRemoved:
		}
	}()

	var removeControlOnce sync.Once
	removeNotifications := func() {
		removeControlOnce.Do(func() {
			removeSignals()
			close(notificationsRemoved)
			<-notificationsDone
			cancelCommand(nil)
		})
	}
	return commandContext, forceStop, removeNotifications
}

func getTerminationError(commandContext context.Context) error {
	interruptionCause := context.Cause(commandContext)
	if _, interrupted := getTerminationExitCode(interruptionCause); interrupted {
		return interruptionCause
	}
	return nil
}

func getTerminationExitCode(runError error) (int, bool) {
	var interruptError engine.InterruptError
	if !errors.As(runError, &interruptError) {
		return 0, false
	}
	interruptSignal, ok := interruptError.Signal.(syscall.Signal)
	if !ok {
		return 1, true
	}
	return 128 + int(interruptSignal), true
}
