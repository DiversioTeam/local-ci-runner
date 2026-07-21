//go:build !darwin && !linux

package main

func getProcessAlive(_ int) (bool, bool) {
	return false, false
}
