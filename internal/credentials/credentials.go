// Package credentials resolves named provider credential profiles without
// persisting secret values in ordinary application state.
package credentials

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/provider"
	"github.com/zalando/go-keyring"
)

const keyringService = "app.schooner.cli.providers"

var profileNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,46}[a-z0-9])?$`)

type Status string

const (
	StatusConnected      Status = "connected"
	StatusActionRequired Status = "action_required"
)

type Profile struct {
	Ref           provider.CredentialProfileRef
	Provider      provider.ID
	Name          string
	ExternalID    string
	AccountName   string
	AccountEmail  string
	CredentialKey string
	Default       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Status        Status
	Warning       string
}

type Credential struct {
	Profile provider.CredentialProfileRef
	Token   string
	Account provider.Account
	Source  string
}

type Store interface {
	ListCredentialProfiles(context.Context) ([]Profile, error)
	FindCredentialProfile(context.Context, provider.CredentialProfileRef) (Profile, error)
	SaveCredentialProfile(context.Context, Profile) error
}

type SecretStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

type KeyringStore struct{}

func (KeyringStore) Get(key string) (string, error) {
	value, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return value, err
}

func (KeyringStore) Set(key, value string) error { return keyring.Set(keyringService, key, value) }

func (KeyringStore) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type Manager struct {
	store     Store
	secrets   SecretStore
	cloud     provider.Cloud
	getenv    func(string) string
	now       func() time.Time
	mu        sync.Mutex
	ephemeral map[provider.CredentialProfileRef]string
}

func New(store Store, secrets SecretStore, cloud provider.Cloud) *Manager {
	return &Manager{store: store, secrets: secrets, cloud: cloud, getenv: os.Getenv, now: time.Now, ephemeral: map[provider.CredentialProfileRef]string{}}
}

func ValidateProfileName(value string) error {
	if !profileNamePattern.MatchString(value) {
		return fmt.Errorf("profile name must be a lowercase slug of 1–48 characters")
	}
	return nil
}

func ParseRef(value string) (provider.CredentialProfileRef, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] != string(provider.DigitalOcean) || ValidateProfileName(parts[1]) != nil {
		return "", fmt.Errorf("credential profile must look like digitalocean/personal")
	}
	return provider.CredentialProfileRef(value), nil
}

func (m *Manager) Connect(ctx context.Context, name, token string, storeSecret, makeDefault bool) (Profile, error) {
	if ValidateProfileName(name) != nil {
		return Profile{}, box.NewError("invalid_input", "credential profile name must be a lowercase slug", nil)
	}
	token = strings.TrimSpace(token)
	if !validDigitalOceanToken(token) {
		return Profile{}, box.NewError("invalid_input", "DigitalOcean personal access token must begin with dop_v1_", nil)
	}
	account, err := m.cloud.Verify(ctx, token)
	if err != nil {
		return Profile{}, err
	}
	ref := provider.ProfileRef(provider.DigitalOcean, name)
	profiles, err := m.store.ListCredentialProfiles(ctx)
	if err != nil {
		return Profile{}, err
	}
	for _, candidate := range profiles {
		if candidate.Provider == provider.DigitalOcean && candidate.ExternalID == account.ExternalID && candidate.Ref != ref {
			return Profile{}, box.NewError("conflict", fmt.Sprintf("this DigitalOcean team is already connected as %s", candidate.Ref), nil)
		}
	}
	existing, findErr := m.store.FindCredentialProfile(ctx, ref)
	if findErr == nil && existing.ExternalID != "" && existing.ExternalID != account.ExternalID {
		return Profile{}, box.NewError("conflict", "credential profile is bound to a different DigitalOcean team", nil)
	}
	if findErr != nil && !box.IsNotFound(findErr) {
		return Profile{}, findErr
	}
	if box.IsNotFound(findErr) && !makeDefault {
		makeDefault = true
		for _, candidate := range profiles {
			if candidate.Provider == provider.DigitalOcean {
				makeDefault = false
				break
			}
		}
	}
	now := m.now().UTC()
	profile := existing
	profile.Ref, profile.Provider, profile.Name = ref, provider.DigitalOcean, name
	profile.ExternalID, profile.AccountName, profile.AccountEmail = account.ExternalID, account.Name, account.Email
	profile.UpdatedAt = now
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	if makeDefault {
		profile.Default = true
	}
	oldKey := profile.CredentialKey
	newKey := ""
	if storeSecret {
		newKey, err = credentialKey()
		if err != nil {
			return Profile{}, box.NewError("internal", "could not create credential reference", err)
		}
		if err = m.secrets.Set(newKey, token); err != nil {
			newKey = ""
			profile.Warning = "Operating-system credential storage is unavailable; the token is usable only for this Schooner process."
			m.mu.Lock()
			m.ephemeral[ref] = token
			m.mu.Unlock()
		} else {
			profile.CredentialKey = newKey
		}
	}
	if err = m.store.SaveCredentialProfile(ctx, profile); err != nil {
		if newKey != "" {
			_ = m.secrets.Delete(newKey)
		}
		m.mu.Lock()
		delete(m.ephemeral, ref)
		m.mu.Unlock()
		return Profile{}, err
	}
	if newKey != "" {
		m.mu.Lock()
		delete(m.ephemeral, ref)
		m.mu.Unlock()
	}
	if newKey != "" && oldKey != "" && oldKey != newKey {
		_ = m.secrets.Delete(oldKey)
	}
	profile.Status = StatusActionRequired
	if profile.CredentialKey != "" || m.ephemeralToken(profile.Ref) != "" {
		profile.Status = StatusConnected
	}
	return profile, nil
}

// Resolve follows the documented environment -> selected -> default order.
// Environment credentials are verified and bound to profile metadata but are
// never written to the secret store.
func (m *Manager) Resolve(ctx context.Context, requested provider.CredentialProfileRef) (Credential, error) {
	token := strings.TrimSpace(m.getenv("DIGITALOCEAN_TOKEN"))
	if token != "" {
		if !validDigitalOceanToken(token) {
			return Credential{}, box.NewError("authentication_required", "DIGITALOCEAN_TOKEN is not a valid DigitalOcean personal access token", nil)
		}
		account, err := m.cloud.Identify(ctx, token)
		if err != nil {
			return Credential{}, err
		}
		ref, profile, err := m.resolveProfile(ctx, requested, account)
		if err != nil {
			return Credential{}, err
		}
		if profile.Ref == "" {
			now := m.now().UTC()
			profile = Profile{Ref: ref, Provider: provider.DigitalOcean, Name: strings.TrimPrefix(string(ref), "digitalocean/"), ExternalID: account.ExternalID, AccountName: account.Name, AccountEmail: account.Email, CreatedAt: now, UpdatedAt: now, Default: true}
			if err = m.store.SaveCredentialProfile(ctx, profile); err != nil {
				return Credential{}, err
			}
		} else {
			profile.ExternalID, profile.AccountName, profile.AccountEmail = account.ExternalID, account.Name, account.Email
			profile.UpdatedAt = m.now().UTC()
			if err = m.store.SaveCredentialProfile(ctx, profile); err != nil {
				return Credential{}, err
			}
		}
		return Credential{Profile: ref, Token: token, Account: account, Source: "environment"}, nil
	}

	profile, err := m.selectedProfile(ctx, requested)
	if err != nil {
		return Credential{}, err
	}
	if token = m.ephemeralToken(profile.Ref); token != "" {
		account, identifyErr := m.cloud.Identify(ctx, token)
		if identifyErr != nil {
			return Credential{}, identifyErr
		}
		if account.ExternalID != profile.ExternalID {
			return Credential{}, box.NewError("conflict", "credential profile now resolves to a different DigitalOcean team", nil)
		}
		return Credential{Profile: profile.Ref, Token: token, Account: account, Source: "memory"}, nil
	}
	if profile.CredentialKey == "" {
		return Credential{}, box.NewError("authentication_required", fmt.Sprintf("credential profile %s needs to be reconnected", profile.Ref), nil)
	}
	token, err = m.secrets.Get(profile.CredentialKey)
	if err != nil {
		return Credential{}, box.NewError("authentication_required", "operating-system credential storage is unavailable", err)
	}
	if token == "" {
		return Credential{}, box.NewError("authentication_required", fmt.Sprintf("credential profile %s needs to be reconnected", profile.Ref), nil)
	}
	if !validDigitalOceanToken(token) {
		return Credential{}, box.NewError("authentication_required", fmt.Sprintf("credential profile %s contains an invalid token and needs to be reconnected", profile.Ref), nil)
	}
	account, err := m.cloud.Identify(ctx, token)
	if err != nil {
		return Credential{}, err
	}
	if account.ExternalID != profile.ExternalID {
		return Credential{}, box.NewError("conflict", "credential profile now resolves to a different DigitalOcean team", nil)
	}
	return Credential{Profile: profile.Ref, Token: token, Account: account, Source: "keyring"}, nil
}

func (m *Manager) List(ctx context.Context) ([]Profile, error) {
	profiles, err := m.store.ListCredentialProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for i := range profiles {
		profiles[i].Status = StatusActionRequired
		if m.ephemeralToken(profiles[i].Ref) != "" {
			profiles[i].Status = StatusConnected
			continue
		}
		if profiles[i].CredentialKey == "" {
			continue
		}
		if value, getErr := m.secrets.Get(profiles[i].CredentialKey); getErr == nil && validDigitalOceanToken(value) {
			profiles[i].Status = StatusConnected
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Ref < profiles[j].Ref })
	return profiles, nil
}

func (m *Manager) Disconnect(ctx context.Context, ref provider.CredentialProfileRef) (Profile, error) {
	profile, err := m.store.FindCredentialProfile(ctx, ref)
	if err != nil {
		return Profile{}, err
	}
	if profile.CredentialKey != "" {
		if err = m.secrets.Delete(profile.CredentialKey); err != nil {
			return Profile{}, box.NewError("internal", "could not remove credential from operating-system storage", err)
		}
	}
	profile.CredentialKey = ""
	m.mu.Lock()
	delete(m.ephemeral, ref)
	m.mu.Unlock()
	profile.UpdatedAt = m.now().UTC()
	if err = m.store.SaveCredentialProfile(ctx, profile); err != nil {
		return Profile{}, err
	}
	profile.Status = StatusActionRequired
	return profile, nil
}

func (m *Manager) ephemeralToken(ref provider.CredentialProfileRef) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ephemeral[ref]
}

func (m *Manager) selectedProfile(ctx context.Context, requested provider.CredentialProfileRef) (Profile, error) {
	if requested != "" {
		return m.store.FindCredentialProfile(ctx, requested)
	}
	profiles, err := m.store.ListCredentialProfiles(ctx)
	if err != nil {
		return Profile{}, err
	}
	for _, profile := range profiles {
		if profile.Provider == provider.DigitalOcean && profile.Default {
			return profile, nil
		}
	}
	return Profile{}, box.NewError("authentication_required", "select a DigitalOcean credential profile or connect one first", nil)
}

func (m *Manager) resolveProfile(ctx context.Context, requested provider.CredentialProfileRef, account provider.Account) (provider.CredentialProfileRef, Profile, error) {
	if requested == "" {
		profiles, err := m.store.ListCredentialProfiles(ctx)
		if err != nil {
			return "", Profile{}, err
		}
		for _, profile := range profiles {
			if profile.Provider == provider.DigitalOcean && profile.Default {
				requested = profile.Ref
				break
			}
		}
		if requested == "" {
			requested = provider.ProfileRef(provider.DigitalOcean, "default")
		}
	}
	profile, err := m.store.FindCredentialProfile(ctx, requested)
	if box.IsNotFound(err) {
		return requested, Profile{}, nil
	}
	if err != nil {
		return "", Profile{}, err
	}
	if profile.ExternalID != "" && profile.ExternalID != account.ExternalID {
		return "", Profile{}, box.NewError("conflict", "environment credential belongs to a different DigitalOcean team than the selected profile", nil)
	}
	return requested, profile, nil
}

func validDigitalOceanToken(value string) bool {
	return len(value) >= 32 && len(value) <= 512 && strings.HasPrefix(value, "dop_v1_") && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-'
	}) == -1
}

func credentialKey() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "provider:digitalocean:credential:" + hex.EncodeToString(value), nil
}
