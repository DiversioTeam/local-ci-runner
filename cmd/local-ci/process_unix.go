//go:build darwin || linux

package main

import (
	"errors"
	"syscall"
)

func getProcessAlive(processID int) (bool, bool) {
	err := syscall.Kill(processID, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, true
	case errors.Is(err, syscall.ESRCH):
		return false, true
	default:
		return false, false
	}
}
