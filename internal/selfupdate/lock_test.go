package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrentStaleLockObserversLeaveOwnerUntouched(t *testing.T) {
	target := filepath.Join(t.TempDir(), "schooner")
	directory := filepath.Join(filepath.Dir(target), lockDirectoryName)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := lockOwner{host: "test-host", pid: 42, target: target, token: "dead"}
	if err := writeLockOwner(filepath.Join(directory, "owner"), owner); err != nil {
		t.Fatal(err)
	}
	observed := make(chan struct{})
	resume := make(chan struct{})
	slow := &updater{
		executablePath: target,
		hostname:       func() (string, error) { return "test-host", nil },
		processAlive: func(int) (bool, error) {
			close(observed)
			<-resume
			return false, nil
		},
	}
	fast := &updater{
		executablePath: target,
		hostname:       func() (string, error) { return "test-host", nil },
		processAlive:   func(int) (bool, error) { return false, nil },
	}
	type result struct {
		lock *installationLock
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		lock, err := slow.acquireLock()
		finished <- result{lock, err}
	}()
	<-observed
	first, firstErr := fast.acquireLock()
	defer first.release()
	close(resume)
	second := <-finished
	defer second.lock.release()
	if ErrorCode(firstErr) != CodeLocked || ErrorCode(second.err) != CodeLocked {
		t.Fatalf("stale observers must both refuse acquisition: first=%v second=%v", firstErr, second.err)
	}
	if got, err := readLockOwner(filepath.Join(directory, "owner")); err != nil || got != owner {
		t.Fatalf("stale owner changed: %+v, %v", got, err)
	}
}
