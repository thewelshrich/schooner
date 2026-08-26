//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
)

func TestTerminationSignalsIncludeHangup(t *testing.T) {
	if !slicesContainsSignal(terminationSignals(), os.Signal(syscall.SIGHUP)) {
		t.Fatal("SIGHUP does not trigger graceful cancellation")
	}
}

func slicesContainsSignal(signals []os.Signal, target os.Signal) bool {
	for _, current := range signals {
		if current == target {
			return true
		}
	}
	return false
}
