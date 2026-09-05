package acquisition_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thewelshrich/schooner/internal/acquisition"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/credentials"
	"github.com/thewelshrich/schooner/internal/inventory/sqlite"
	"github.com/thewelshrich/schooner/internal/provider"
)

// Cancel precisely after the real SQLite Box transaction commits, before
// acquisition.Service can delete the provisioning checkpoint.
type cancelAfterBoxCommit struct {
	*sqlite.Store
	cancel context.CancelFunc
}

func (s *cancelAfterBoxCommit) CompleteAdd(ctx context.Context, op box.AddOperation, record box.Record, observation box.Observation) error {
	if err := s.Store.CompleteAdd(ctx, op, record, observation); err != nil {
		return err
	}
	s.cancel()
	return nil
}

func TestInterruptedProvisionCompletion(t *testing.T) {
	for _, cleanup := range []string{"resume", "resume-online", "legacy-resume", "mismatch", "remove", "destroy", "race-remove", "race-readd", "race-replace-box"} {
		t.Run(cleanup, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "state.db")
			db, err := sqlite.Open(t.Context(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &cancelAfterBoxCommit{Store: db, cancel: cancel}
			runtime := &testRuntime{capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Home: "/root", WorktreeRoot: "/root/schooner", WorktreeRootExists: true, Git: box.Tool{Available: true}, Tmux: box.Tool{Available: true}}}
			cloud := &testCloud{warning: "temporary provider SSH key cleanup required"}
			if cleanup == "legacy-resume" {
				cloud.warning = ""
			}
			svc := acquisition.New(box.New(runtime, store), store, testResolver{}, cloud, testIdentity{}, &testWaiter{})
			request := acquisition.ProvisionRequest{Name: "work", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", WorktreeRoot: box.DefaultWorktreeRoot}
			_, err = svc.Provision(ctx, request)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected cancellation after commit, got %v", err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}

			// A fresh connection and services simulate a new invocation, with no fault.
			db, err = sqlite.Open(t.Context(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			record, err := db.FindByName(t.Context(), "work")
			if err != nil {
				t.Fatalf("Box was not committed: %v", err)
			}
			op, err := db.FindProvision(t.Context(), "work")
			if err != nil || op.ResourceID != record.ProviderResourceID || op.Checkpoint != "remote_preparation" {
				t.Fatalf("unexpected persisted checkpoint: %+v, %v", op, err)
			}
			boxes := box.New(runtime, db)
			svc = acquisition.New(boxes, db, testResolver{}, cloud, testIdentity{}, &testWaiter{})
			if cleanup == "mismatch" {
				op.ResourceID = "different-resource"
				if err := db.CheckpointProvision(t.Context(), op); err != nil {
					t.Fatal(err)
				}
			}
			cloud.provision = provider.ProvisionRequest{}
			if cleanup == "mismatch" {
				if _, err := svc.Provision(t.Context(), request); box.ErrorCode(err) != "conflict" {
					t.Fatalf("mismatched resource must conflict: %v", err)
				}
				if _, err := db.FindProvision(t.Context(), "work"); err != nil {
					t.Fatalf("mismatched checkpoint must remain: %v", err)
				}
				if cloud.provision.Name != "" {
					t.Fatal("mismatched completion contacted provider")
				}
				return
			}
			if cleanup == "race-remove" || cleanup == "race-readd" || cleanup == "race-replace-box" {
				raced := &afterObservationStore{Store: db, after: func() {
					if _, err := db.Remove(t.Context(), "work"); err != nil {
						t.Fatal(err)
					}
					replacement := op
					if cleanup == "race-readd" {
						replacement.CorrelationID = "replacement"
					}
					if _, err := db.BeginProvision(t.Context(), replacement); err != nil {
						t.Fatal(err)
					}
					if cleanup == "race-replace-box" {
						record.ID = "replacement-box"
						if err := db.CompleteAdd(t.Context(), box.AddOperation{Name: record.Name}, record, box.Observation{BoxID: record.ID}); err != nil {
							t.Fatal(err)
						}
					}
				}}
				svc = acquisition.New(boxes, raced, testResolver{}, cloud, testIdentity{}, &testWaiter{})
				if _, err := svc.Provision(t.Context(), request); box.ErrorCode(err) != "conflict" {
					t.Fatalf("stale completion = %v", err)
				}
				pending, err := db.FindProvision(t.Context(), "work")
				if err != nil {
					t.Fatalf("concurrent checkpoint lost: %v", err)
				}
				if cleanup == "race-readd" && pending.CorrelationID != "replacement" {
					t.Fatalf("replacement = %+v", pending)
				}
				return
			}
			if cleanup == "resume" || cleanup == "resume-online" || cleanup == "legacy-resume" {
				if cleanup != "resume-online" {
					svc = acquisition.New(boxes, db, unavailableDependencies{}, cloud, unavailableDependencies{}, &testWaiter{})
				}
				result, retryErr := svc.Provision(t.Context(), request)
				if retryErr != nil || result.Box.ID != record.ID || result.Capabilities.OSID != "ubuntu" {
					t.Fatalf("resume = %+v, %v", result, retryErr)
				}
				if result.Warning != cloud.warning || !slices.Equal(result.Verified, []string{"git", "schooner", "tmux"}) {
					t.Fatalf("recovered warning/verified = %q, %v", result.Warning, result.Verified)
				}
				if _, err := db.FindProvision(t.Context(), "work"); !box.IsNotFound(err) {
					t.Fatalf("completed checkpoint remains: %v", err)
				}
				if cloud.provision.Name != "" {
					t.Fatal("completion contacted provider")
				}
				return
			}

			if cleanup == "remove" {
				_, err = boxes.Remove(t.Context(), "work")
			} else {
				_, err = svc.Destroy(t.Context(), "work")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.FindByName(t.Context(), "work"); !box.IsNotFound(err) {
				t.Fatalf("cleanup did not remove Box: %v", err)
			}
			request.Size = "different-size"
			_, reuseErr := svc.Provision(t.Context(), request)
			t.Logf("after %s, name reuse with new selections: %v", cleanup, reuseErr)
			if reuseErr != nil {
				t.Errorf("REGRESSION: %s left obsolete provisioning selections attached to the removed Box name", cleanup)
			}
		})
	}
}

type testResolver struct{}

func (testResolver) Resolve(context.Context, provider.CredentialProfileRef) (credentials.Credential, error) {
	return credentials.Credential{Profile: "digitalocean/default", Token: "token", Account: provider.Account{ExternalID: "team-1"}}, nil
}

type testIdentity struct{}

func (testIdentity) Ensure(context.Context) (acquisition.Identity, error) {
	return acquisition.Identity{PublicKey: "ssh-ed25519 AAAA", PrivateKey: "/state/id_ed25519"}, nil
}

type testWaiter struct{ connection box.Connection }

func (w *testWaiter) WaitReady(_ context.Context, connection box.Connection) error {
	w.connection = connection
	return nil
}

type testCloud struct {
	provision provider.ProvisionRequest
	destroyed bool
	warning   string
}

func (*testCloud) Identify(context.Context, string) (provider.Account, error) {
	return provider.Account{}, nil
}

func (*testCloud) Verify(context.Context, string) (provider.Account, error) {
	return provider.Account{}, nil
}
func (*testCloud) Catalog(context.Context, string) (provider.Catalog, error) {
	return provider.Catalog{}, nil
}
func (f *testCloud) Provision(_ context.Context, _ string, request provider.ProvisionRequest) (provider.ProvisionedMachine, error) {
	f.provision = request
	return provider.ProvisionedMachine{ResourceID: "42", PublicIPv4: "203.0.113.8", SSHUsername: "root", Warning: f.warning}, nil
}
func (*testCloud) Inspect(_ context.Context, _ string, ref provider.ResourceRef) (provider.Resource, error) {
	return provider.Resource{ID: ref.ResourceID, CorrelationID: ref.CorrelationID}, nil
}
func (f *testCloud) Destroy(context.Context, string, provider.ResourceRef) error {
	f.destroyed = true
	return nil
}

type testRuntime struct {
	capabilities box.Capabilities
	connection   box.Connection
}

func (f *testRuntime) Resolve(_ context.Context, connection box.Connection) error {
	f.connection = connection
	return nil
}
func (f *testRuntime) Inspect(_ context.Context, connection box.Connection, _ string) (box.Capabilities, error) {
	f.connection = connection
	return f.capabilities, nil
}
func (f *testRuntime) EnsureIdentity(_ context.Context, connection box.Connection, candidate string) (string, error) {
	f.connection = connection
	f.capabilities.RemoteIdentity = candidate
	return candidate, nil
}
func (f *testRuntime) EnsureHost(_ context.Context, connection box.Connection, request box.HostInstallRequest) (box.HostInstallResult, error) {
	f.connection = connection
	f.capabilities.Host = box.HostRuntime{Path: request.Path, Version: "v1.2.3", ProtocolVersion: "1", Capabilities: []string{"host.configure.v1", "host.doctor.v1", "host.hello.v1", "host.inspect.v2", "worktree.inspect.v1", "worktree.list.v1"}}
	return box.HostInstallResult{Runtime: f.capabilities.Host, TargetVersion: "v1.2.3", Action: box.HostInstalled}, nil
}
func (f *testRuntime) InspectHost(_ context.Context, connection box.Connection, installed box.HostRuntime, _ string, _ string) (box.Capabilities, error) {
	f.connection = connection
	f.capabilities.Host = installed
	return f.capabilities, nil
}
func (*testRuntime) InstallTools(context.Context, box.Connection, []string) error { return nil }
func (f *testRuntime) EnsureWorktreeRoot(_ context.Context, connection box.Connection, _ string) (string, error) {
	f.connection = connection
	return "/root/schooner", nil
}

func (f *testRuntime) ConfigureHost(context.Context, box.Connection, box.HostRuntime, string, string) error {
	return nil
}

// Recovery must work even when credentials and local key material are unavailable.
type unavailableDependencies struct{}

func (unavailableDependencies) Resolve(context.Context, provider.CredentialProfileRef) (credentials.Credential, error) {
	return credentials.Credential{}, errors.New("credentials unavailable")
}
func (unavailableDependencies) Ensure(context.Context) (acquisition.Identity, error) {
	return acquisition.Identity{}, errors.New("identity unavailable")
}

type afterObservationStore struct {
	*sqlite.Store
	after func()
}

func (s *afterObservationStore) LastObservation(ctx context.Context, id string) (box.Observation, error) {
	observation, err := s.Store.LastObservation(ctx, id)
	if err == nil {
		s.after()
	}
	return observation, err
}
