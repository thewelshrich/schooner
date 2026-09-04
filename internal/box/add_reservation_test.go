package box_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/inventory/sqlite"
)

func TestAddPreflightFailureAllowsCorrectedDestination(t *testing.T) {
	failure := errors.New("preflight failed")
	for _, step := range []string{"resolve", "connect", "certify"} {
		t.Run(step, func(t *testing.T) {
			store := reservationStore(t)
			runtime := &reservationRuntime{}
			switch step {
			case "resolve":
				runtime.resolveErr = failure
			case "connect":
				runtime.inspectErr = failure
			case "certify":
				runtime.unsupported = true
			}
			service := box.New(runtime, store)
			if _, err := service.Add(t.Context(), box.AddRequest{Name: "work", SSHDestination: "typo"}); err == nil {
				t.Fatal("preflight unexpectedly succeeded")
			}
			runtime.resolveErr, runtime.inspectErr, runtime.unsupported = nil, nil, false
			mutation := errors.New("identity mutation attempted")
			runtime.ensureIdentity = func() error { return mutation }
			if _, err := service.Add(t.Context(), box.AddRequest{Name: "work", SSHDestination: "correct"}); !errors.Is(err, mutation) {
				t.Fatalf("corrected destination did not reach identity setup: %v", err)
			}
		})
	}
}

func TestAddRetainsReservationOnceMutationIsPossible(t *testing.T) {
	store := reservationStore(t)
	mutation := errors.New("identity mutation interrupted")
	runtime := &reservationRuntime{ensureIdentity: func() error { return mutation }}
	service := box.New(runtime, store)
	request := box.AddRequest{Name: "work", SSHDestination: "original"}
	if _, err := service.Add(t.Context(), request); !errors.Is(err, mutation) {
		t.Fatalf("initial add: %v", err)
	}
	// A failed connection on retry must not discard an earlier mutation's reservation.
	runtime.resolveErr = errors.New("offline")
	if _, err := service.Add(t.Context(), request); !errors.Is(err, runtime.resolveErr) {
		t.Fatalf("offline retry: %v", err)
	}
	runtime.resolveErr = nil
	request.SSHDestination = "other"
	if _, err := service.Add(t.Context(), request); box.ErrorCode(err) != "conflict" {
		t.Fatalf("changed recovery input: %v", err)
	}
	request.SSHDestination = "original"
	if _, err := service.Add(t.Context(), request); !errors.Is(err, mutation) {
		t.Fatalf("matching recovery input: %v", err)
	}
}

func TestAddReservesNameBeforeConcurrentMutation(t *testing.T) {
	store := reservationStore(t)
	otherRuntime := &reservationRuntime{ensureIdentity: func() error {
		t.Fatal("competing destination reached mutation")
		return nil
	}}
	other := box.New(otherRuntime, store)
	interrupted := errors.New("interrupted")
	runtime := &reservationRuntime{ensureIdentity: func() error {
		if _, err := other.Add(t.Context(), box.AddRequest{Name: "work", SSHDestination: "other"}); box.ErrorCode(err) != "conflict" {
			t.Fatalf("overlapping add: %v", err)
		}
		return interrupted
	}}
	if _, err := box.New(runtime, store).Add(t.Context(), box.AddRequest{Name: "work", SSHDestination: "original"}); !errors.Is(err, interrupted) {
		t.Fatalf("initial add: %v", err)
	}
}

func reservationStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Embedding the interface makes any unexpected operation beyond identity fail.
type reservationRuntime struct {
	box.Runtime
	resolveErr     error
	inspectErr     error
	unsupported    bool
	ensureIdentity func() error
}

func (r *reservationRuntime) Resolve(context.Context, box.Connection) error {
	return r.resolveErr
}

func (r *reservationRuntime) Inspect(context.Context, box.Connection, string) (box.Capabilities, error) {
	capabilities := box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64"}
	if r.unsupported {
		capabilities.OSID = "unsupported"
	}
	return capabilities, r.inspectErr
}

func (r *reservationRuntime) EnsureIdentity(context.Context, box.Connection, string) (string, error) {
	return "", r.ensureIdentity()
}
