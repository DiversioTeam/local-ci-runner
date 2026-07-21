package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/DiversioTeam/local-ci-runner/internal/engine"
)

func TestTerminationControlCancelsThenForces(t *testing.T) {
	terminationSignals := make(chan os.Signal, 2)
	removed := false
	commandContext, forceStop, removeNotifications := buildTerminationControl(t.Context(), terminationSignals, func() {
		removed = true
	})
	defer removeNotifications()

	terminationSignals <- os.Interrupt
	select {
	case <-commandContext.Done():
	case <-time.After(time.Second):
		t.Fatal("termination context was not canceled")
	}

	var interruptError engine.InterruptError
	if !errors.As(context.Cause(commandContext), &interruptError) {
		t.Fatalf("context cause = %v, want InterruptError", context.Cause(commandContext))
	}
	if interruptError.Signal != os.Interrupt {
		t.Fatalf("interrupt signal = %v, want %v", interruptError.Signal, os.Interrupt)
	}

	terminationSignals <- syscall.SIGTERM
	select {
	case <-forceStop:
	case <-time.After(time.Second):
		t.Fatal("force stop was not requested")
	}

	removeNotifications()
	if !removed {
		t.Fatal("signal notifications were not removed")
	}
}

func TestTerminationControlCleanupIsIdempotent(t *testing.T) {
	terminationSignals := make(chan os.Signal, 2)
	removeCount := 0
	commandContext, forceStop, removeNotifications := buildTerminationControl(t.Context(), terminationSignals, func() {
		removeCount++
	})

	removeNotifications()
	removeNotifications()
	if got, want := removeCount, 1; got != want {
		t.Fatalf("remove count = %d, want %d", got, want)
	}
	if terminationError := getTerminationError(commandContext); terminationError != nil {
		t.Fatalf("getTerminationError() = %v, want nil", terminationError)
	}
	select {
	case <-forceStop:
		t.Fatal("normal cleanup requested force stop")
	default:
	}
}

func TestGetTerminationErrorReturnsInterruptionCause(t *testing.T) {
	commandContext, cancelCommand := context.WithCancelCause(t.Context())
	cancelCommand(engine.InterruptError{Signal: syscall.SIGTERM})

	returnedError := getTerminationError(commandContext)
	var interruptError engine.InterruptError
	if !errors.As(returnedError, &interruptError) {
		t.Fatalf("getTerminationError() = %v, want InterruptError", returnedError)
	}
}

func TestGetTerminationErrorIgnoresNormalContextCleanup(t *testing.T) {
	commandContext, cancelCommand := context.WithCancelCause(t.Context())
	cancelCommand(nil)

	if returnedError := getTerminationError(commandContext); returnedError != nil {
		t.Fatalf("getTerminationError() = %v, want nil", returnedError)
	}
}

func TestGetTerminationExitCode(t *testing.T) {
	testCases := []struct {
		name   string
		signal os.Signal
		want   int
	}{
		{name: "SIGINT", signal: os.Interrupt, want: 130},
		{name: "SIGTERM", signal: syscall.SIGTERM, want: 143},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			exitCode, interrupted := getTerminationExitCode(engine.InterruptError{Signal: testCase.signal})
			if !interrupted {
				t.Fatal("getTerminationExitCode() did not recognize interruption")
			}
			if exitCode != testCase.want {
				t.Fatalf("getTerminationExitCode() = %d, want %d", exitCode, testCase.want)
			}
		})
	}
}
