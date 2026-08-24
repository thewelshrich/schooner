package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/provider"
)

func TestConnectStoresSecretOutsideProfileAndMakesFirstDefault(t *testing.T) {
	store := &memoryStore{profiles: map[provider.CredentialProfileRef]Profile{}}
	secrets := &memorySecrets{values: map[string]string{}}
	manager := New(store, secrets, fakeCloud{account: provider.Account{ExternalID: "team-1", Name: "Personal"}})
	profile, err := manager.Connect(t.Context(), "personal", testToken("one"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Default || profile.Status != StatusConnected {
		t.Fatalf("profile=%+v", profile)
	}
	if strings.Contains(profile.CredentialKey, testToken("one")) {
		t.Fatal("token leaked into credential reference")
	}
	if len(secrets.values) != 1 {
		t.Fatalf("secrets=%v", secrets.values)
	}
	if stored := store.profiles[profile.Ref]; stored.ExternalID != "team-1" || stored.CredentialKey == "" {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestEnvironmentCredentialIsNeverStoredAndCannotCrossTeams(t *testing.T) {
	store := &memoryStore{profiles: map[provider.CredentialProfileRef]Profile{"digitalocean/work": {Ref: "digitalocean/work", Provider: provider.DigitalOcean, Name: "work", ExternalID: "team-1", Default: true}}}
	secrets := &memorySecrets{values: map[string]string{}}
	cloud := fakeCloud{account: provider.Account{ExternalID: "team-2"}}
	manager := New(store, secrets, cloud)
	manager.getenv = func(string) string { return testToken("env") }
	_, err := manager.Resolve(t.Context(), "digitalocean/work")
	if box.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
	if len(secrets.values) != 0 {
		t.Fatal("environment token was stored")
	}
}

func TestDisconnectRetainsActionRequiredProfile(t *testing.T) {
	store := &memoryStore{profiles: map[provider.CredentialProfileRef]Profile{}}
	secrets := &memorySecrets{values: map[string]string{}}
	manager := New(store, secrets, fakeCloud{account: provider.Account{ExternalID: "team-1"}})
	profile, err := manager.Connect(t.Context(), "personal", testToken("one"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	profile, err = manager.Disconnect(t.Context(), profile.Ref)
	if err != nil || profile.Status != StatusActionRequired || store.profiles[profile.Ref].CredentialKey != "" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
}

func TestUnavailableKeyringFallsBackToCurrentProcessMemory(t *testing.T) {
	store := &memoryStore{profiles: map[provider.CredentialProfileRef]Profile{}}
	secrets := &memorySecrets{values: map[string]string{}, setErr: errors.New("locked")}
	manager := New(store, secrets, fakeCloud{account: provider.Account{ExternalID: "team-1"}})
	profile, err := manager.Connect(t.Context(), "personal", testToken("one"), true, false)
	if err != nil || profile.Warning == "" || profile.CredentialKey != "" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	credential, err := manager.Resolve(t.Context(), profile.Ref)
	if err != nil || credential.Source != "memory" || credential.Token != testToken("one") {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

type memoryStore struct {
	profiles map[provider.CredentialProfileRef]Profile
}

func (m *memoryStore) ListCredentialProfiles(context.Context) ([]Profile, error) {
	result := []Profile{}
	for _, profile := range m.profiles {
		result = append(result, profile)
	}
	return result, nil
}
func (m *memoryStore) FindCredentialProfile(_ context.Context, ref provider.CredentialProfileRef) (Profile, error) {
	value, ok := m.profiles[ref]
	if !ok {
		return Profile{}, box.NotFound(string(ref))
	}
	return value, nil
}
func (m *memoryStore) SaveCredentialProfile(_ context.Context, profile Profile) error {
	if profile.Default {
		for ref, candidate := range m.profiles {
			candidate.Default = false
			m.profiles[ref] = candidate
		}
	}
	m.profiles[profile.Ref] = profile
	return nil
}

type memorySecrets struct {
	values map[string]string
	setErr error
}

func (m *memorySecrets) Get(key string) (string, error) { return m.values[key], nil }
func (m *memorySecrets) Set(key, value string) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.values[key] = value
	return nil
}
func (m *memorySecrets) Delete(key string) error { delete(m.values, key); return nil }

type fakeCloud struct{ account provider.Account }

func (f fakeCloud) Identify(context.Context, string) (provider.Account, error) {
	return f.account, nil
}

func (f fakeCloud) Verify(context.Context, string) (provider.Account, error) { return f.account, nil }
func (fakeCloud) Catalog(context.Context, string) (provider.Catalog, error) {
	return provider.Catalog{}, nil
}
func (fakeCloud) Provision(context.Context, string, provider.ProvisionRequest) (provider.ProvisionedMachine, error) {
	return provider.ProvisionedMachine{}, nil
}
func (fakeCloud) Inspect(context.Context, string, provider.ResourceRef) (provider.Resource, error) {
	return provider.Resource{}, nil
}
func (fakeCloud) Destroy(context.Context, string, provider.ResourceRef) error { return nil }
func testToken(seed string) string                                            { return "dop_v1_" + seed + strings.Repeat("x", 40) }
