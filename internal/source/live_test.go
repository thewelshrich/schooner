package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/source"
	"github.com/thewelshrich/schooner/internal/source/boxgit"
	sourcegithub "github.com/thewelshrich/schooner/internal/source/github"
)

func TestLiveGitHubSourceAccess(t *testing.T) {
	if os.Getenv("SCHOONER_LIVE_GITHUB") != "1" {
		t.Skip("set SCHOONER_LIVE_GITHUB=1 and the documented per-case variables to run GitHub smoke tests")
	}
	t.Run("public fresh Box clone", func(t *testing.T) {
		repository := requiredLiveValue(t, "SCHOONER_LIVE_GITHUB_PUBLIC_REPOSITORY")
		manager, err := boxgit.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		liveClone(t, manager, repository)
	})
	t.Run("adopted Box ambient credentials", func(t *testing.T) {
		repository := requiredLiveValue(t, "SCHOONER_LIVE_GITHUB_AMBIENT_REPOSITORY")
		manager, err := boxgit.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		liveClone(t, manager, repository)
	})
	t.Run("fresh Box managed private access", func(t *testing.T) {
		token := requiredLiveValue(t, "SCHOONER_LIVE_GITHUB_TOKEN")
		repository := requiredLiveValue(t, "SCHOONER_LIVE_GITHUB_PRIVATE_REPOSITORY")
		liveManagedConnection(t, token, repository, false)
	})
	t.Run("SAML organization", func(t *testing.T) {
		token := requiredLiveValue(t, "SCHOONER_LIVE_GITHUB_TOKEN")
		repository := requiredLiveValue(t, "SCHOONER_LIVE_GITHUB_SAML_REPOSITORY")
		liveManagedConnection(t, token, repository, true)
	})
}

func liveClone(t *testing.T, manager *boxgit.Manager, rawRepository string) {
	t.Helper()
	identity, network, err := source.RepositoryIdentityFor(rawRepository)
	if err != nil || !network {
		t.Fatalf("repository identity = %+v, network=%t, error=%v", identity, network, err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, identity.Repository)
	prepared := 0
	err = manager.Clone(t.Context(), source.CloneExecution{Repository: identity, SuppliedOrigin: rawRepository, WorktreeRoot: root, Destination: destination}, func() error {
		prepared++
		return os.RemoveAll(destination)
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared == 0 {
		t.Fatal("live clone made no transport attempt")
	}
	output, err := os.ReadFile(filepath.Join(destination, ".git", "config"))
	if err != nil || !strings.Contains(string(output), rawRepository) {
		t.Fatalf("clone did not preserve its supplied origin: %v", err)
	}
}

func liveManagedConnection(t *testing.T, token, rawRepository string, expectSAML bool) {
	t.Helper()
	identity, network, err := source.RepositoryIdentityFor(rawRepository)
	if err != nil || !network || !identity.IsGitHub() {
		t.Fatalf("GitHub repository identity = %+v, network=%t, error=%v", identity, network, err)
	}
	host, err := boxgit.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := &liveTarget{name: "live-smoke-" + time.Now().UTC().Format("20060102-150405"), identity: "11111111-1111-4111-8111-111111111111", host: host}
	remote := &liveGitHub{token: source.Token{AccessToken: token}, client: sourcegithub.New(sourcegithub.Options{})}
	store := &liveStore{accounts: map[string]source.Account{}, identities: map[string]source.BoxIdentity{}}
	secrets := &liveSecrets{values: map[string]string{}}
	manager, err := source.NewManager(store, secrets, remote, filepath.Join(t.TempDir(), "locks"))
	if err != nil {
		t.Fatal(err)
	}
	connected := false
	defer func() {
		if _, disconnectErr := manager.Disconnect(context.Background(), source.DisconnectRequest{Target: target, BoxName: target.name, AllowAuthorization: true}); disconnectErr != nil && connected {
			t.Errorf("live source cleanup: %v", disconnectErr)
		}
	}()
	_, err = manager.Connect(t.Context(), source.ConnectRequest{Target: target, AllowAuthorization: true, Repository: identity.CanonicalSSH()})
	if binding, findErr := store.FindBoxSourceIdentity(t.Context(), target.identity, source.GitHub); findErr == nil && binding.RemoteKeyID != 0 {
		connected = true
	}
	if expectSAML {
		var domain *source.Error
		if !errors.As(err, &domain) || domain.Code != "authentication_required" || domain.Context["reason"] != "github_saml_sso" {
			t.Fatalf("SAML error = %#v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	connected = true
}

func requiredLiveValue(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Skipf("set %s to run this live case", name)
	}
	return value
}

type liveTarget struct {
	name     string
	identity string
	host     *boxgit.Manager
}

func (target *liveTarget) BoxName() string     { return target.name }
func (target *liveTarget) BoxIdentity() string { return target.identity }
func (target *liveTarget) InspectSourceIdentity(ctx context.Context, provider string) (source.HostIdentity, error) {
	return target.host.Inspect(ctx, provider)
}
func (target *liveTarget) EnsureSourceIdentity(ctx context.Context, request source.EnsureIdentityRequest) (source.HostIdentity, error) {
	return target.host.Ensure(ctx, request)
}
func (target *liveTarget) RemoveSourceIdentity(ctx context.Context, request source.RemoveIdentityRequest) (source.RemoveIdentityResult, error) {
	return target.host.Remove(ctx, request)
}
func (target *liveTarget) VerifySourceRepository(ctx context.Context, request source.VerifyRequest) (source.VerifyResult, error) {
	return target.host.Verify(ctx, request)
}

type liveGitHub struct {
	token  source.Token
	client *sourcegithub.Client
}

func (client *liveGitHub) Authorize(context.Context) (source.Token, error) { return client.token, nil }
func (client *liveGitHub) Refresh(ctx context.Context, token string) (source.Token, error) {
	return client.client.Refresh(ctx, token)
}
func (client *liveGitHub) Account(ctx context.Context, token string) (source.RemoteAccount, error) {
	return client.client.Account(ctx, token)
}
func (client *liveGitHub) HostKeys(ctx context.Context) ([]source.HostKey, error) {
	return client.client.HostKeys(ctx)
}
func (client *liveGitHub) ListKeys(ctx context.Context, token string) ([]source.RemoteKey, error) {
	return client.client.ListKeys(ctx, token)
}
func (client *liveGitHub) CreateKey(ctx context.Context, token, title, publicKey string) (source.RemoteKey, error) {
	return client.client.CreateKey(ctx, token, title, publicKey)
}
func (client *liveGitHub) DeleteKey(ctx context.Context, token string, id int64) (bool, error) {
	return client.client.DeleteKey(ctx, token, id)
}

type liveStore struct {
	accounts   map[string]source.Account
	identities map[string]source.BoxIdentity
}

func (store *liveStore) FindSourceAccount(_ context.Context, provider string) (source.Account, error) {
	value, ok := store.accounts[provider]
	if !ok {
		return source.Account{}, source.NewError("not_found", "source account is absent", nil)
	}
	return value, nil
}
func (store *liveStore) SaveSourceAccount(_ context.Context, value source.Account) error {
	store.accounts[value.Provider] = value
	return nil
}
func (store *liveStore) DeleteSourceAccount(_ context.Context, provider string) error {
	delete(store.accounts, provider)
	return nil
}
func (store *liveStore) FindBoxSourceIdentity(_ context.Context, identity, provider string) (source.BoxIdentity, error) {
	value, ok := store.identities[identity+"\x00"+provider]
	if !ok {
		return source.BoxIdentity{}, source.NewError("not_found", "Box source identity is absent", nil)
	}
	return value, nil
}
func (store *liveStore) FindBoxSourceIdentityByName(_ context.Context, name, provider string) (source.BoxIdentity, error) {
	for _, value := range store.identities {
		if value.BoxName == name && value.Provider == provider {
			return value, nil
		}
	}
	return source.BoxIdentity{}, source.NewError("not_found", "Box source identity is absent", nil)
}
func (store *liveStore) ListBoxSourceIdentities(_ context.Context, provider string) ([]source.BoxIdentity, error) {
	result := make([]source.BoxIdentity, 0, len(store.identities))
	for _, value := range store.identities {
		if value.Provider == provider {
			result = append(result, value)
		}
	}
	return result, nil
}
func (store *liveStore) SaveBoxSourceIdentity(_ context.Context, value source.BoxIdentity) error {
	store.identities[value.BoxIdentity+"\x00"+value.Provider] = value
	return nil
}
func (store *liveStore) DeleteBoxSourceIdentity(_ context.Context, identity, provider string) error {
	delete(store.identities, identity+"\x00"+provider)
	return nil
}

type liveSecrets struct{ values map[string]string }

func (secrets *liveSecrets) Get(key string) (string, error) { return secrets.values[key], nil }
func (secrets *liveSecrets) Set(key, value string) error {
	secrets.values[key] = value
	return nil
}
func (secrets *liveSecrets) Delete(key string) error {
	delete(secrets.values, key)
	return nil
}
