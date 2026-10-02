package main

import (
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

	// main maps this error to exit status 128+signal.
	var interruptError engine.InterruptError
	if terminationError := getTerminationError(commandContext); !errors.As(terminationError, &interruptError) {
		t.Fatalf("termination error = %v, want InterruptError", terminationError)
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

func TestTerminationControlCleanupIsNotAnInterruption(t *testing.T) {
	terminationSignals := make(chan os.Signal, 2)
	commandContext, forceStop, removeNotifications := buildTerminationControl(t.Context(), terminationSignals, func() {})

	// main removes notifications before asking whether a signal ended the command.
	removeNotifications()
	if terminationError := getTerminationError(commandContext); terminationError != nil {
		t.Fatalf("getTerminationError() = %v, want nil", terminationError)
	}
	select {
	case <-forceStop:
		t.Fatal("normal cleanup requested force stop")
	default:
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
