// Package source manages access from Boxes to source hosts. It owns the
// lifecycle linking one locally authorized Source Account to Box-owned SSH
// identities while keeping credentials and private keys out of inventory.
package source

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thewelshrich/schooner/internal/secretstore"
)

const (
	GitHub                = "github"
	GitHubHost            = "github.com"
	SourceKeyringService  = "app.schooner.cli.sources"
	CodeSourceUnavailable = "source_unavailable"
)

type State string

const (
	StateConnecting     State = "connecting"
	StateConnected      State = "connected"
	StateDisconnecting  State = "disconnecting"
	StateCleanupPending State = "cleanup_pending"
)

type StatusState string

const (
	StatusNotConnected   StatusState = "not_connected"
	StatusConnected      StatusState = "connected"
	StatusActionRequired StatusState = "action_required"
	StatusCleanupPending StatusState = "cleanup_pending"
	StatusConflict       StatusState = "conflict"
	StatusUnknown        StatusState = "unknown"
)

// Account is safe local metadata for one authorized source-host account.
type Account struct {
	Provider             string
	ExternalID           string
	Login                string
	CredentialKey        string
	CredentialGeneration string
	AccessExpiresAt      time.Time
	RefreshExpiresAt     time.Time
	Status               string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// BoxIdentity records only the safe correlation metadata for a keypair owned
// by a Box. Public and private key material are live Box state, not inventory.
type BoxIdentity struct {
	BoxIdentity       string
	BoxName           string
	Provider          string
	AccountExternalID string
	Fingerprint       string
	RemoteKeyID       int64
	RemoteKeyTitle    string
	State             State
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Store interface {
	FindSourceAccount(context.Context, string) (Account, error)
	SaveSourceAccount(context.Context, Account) error
	DeleteSourceAccount(context.Context, string) error
	FindBoxSourceIdentity(context.Context, string, string) (BoxIdentity, error)
	FindBoxSourceIdentityByName(context.Context, string, string) (BoxIdentity, error)
	ListBoxSourceIdentities(context.Context, string) ([]BoxIdentity, error)
	SaveBoxSourceIdentity(context.Context, BoxIdentity) error
	DeleteBoxSourceIdentity(context.Context, string, string) error
}

// Token is the versioned secret payload stored in the operating-system
// credential store. It is never included in command or host-operation results.
type Token struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	AccessExpiresAt  time.Time `json:"access_expires_at,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
}

type DeviceAuthorization struct {
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
}

type DevicePresenter interface {
	Present(context.Context, DeviceAuthorization) error
	Wait(context.Context, string, func(context.Context) error) error
}

type RemoteAccount struct {
	ID    string
	Login string
}

type RemoteKey struct {
	ID          int64
	Title       string
	PublicKey   string
	Fingerprint string
}

type HostKey struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
}

// GitHub is the true-external seam used internally by Manager. Production uses
// the HTTP adapter; tests use a handwritten fake.
type GitHubClient interface {
	Authorize(context.Context) (Token, error)
	Refresh(context.Context, string) (Token, error)
	Account(context.Context, string) (RemoteAccount, error)
	HostKeys(context.Context) ([]HostKey, error)
	ListKeys(context.Context, string) ([]RemoteKey, error)
	CreateKey(context.Context, string, string, string) (RemoteKey, error)
	DeleteKey(context.Context, string, int64) (bool, error)
}

type HostIdentity struct {
	Provider         string   `json:"provider"`
	Exists           bool     `json:"exists"`
	PublicKey        string   `json:"public_key,omitempty"`
	Fingerprint      string   `json:"fingerprint,omitempty"`
	TrustConfigured  bool     `json:"trust_configured"`
	HostFingerprints []string `json:"host_fingerprints,omitempty"`
}

type EnsureIdentityRequest struct {
	Provider string    `json:"provider"`
	HostKeys []HostKey `json:"host_keys"`
}

type RemoveIdentityResult struct {
	Provider string `json:"provider"`
	Removed  bool   `json:"removed"`
}

type RemoveIdentityRequest struct {
	Provider            string `json:"provider"`
	ExpectedFingerprint string `json:"expected_fingerprint"`
}

type VerifyRequest struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository,omitempty"`
}

type VerifyResult struct {
	Provider      string `json:"provider"`
	Authenticated bool   `json:"authenticated"`
}

// Target is the Box-execution seam. boxtarget.Target supplies direct and SSH
// adapters with the same typed behavior.
type Target interface {
	BoxName() string
	BoxIdentity() string
	InspectSourceIdentity(context.Context, string) (HostIdentity, error)
	EnsureSourceIdentity(context.Context, EnsureIdentityRequest) (HostIdentity, error)
	RemoveSourceIdentity(context.Context, RemoveIdentityRequest) (RemoveIdentityResult, error)
	VerifySourceRepository(context.Context, VerifyRequest) (VerifyResult, error)
}

type ConnectPhase string

const (
	ConnectPhaseCreatingKey    ConnectPhase = "creating_key"
	ConnectPhaseRegisteringKey ConnectPhase = "registering_key"
	ConnectPhaseVerifying      ConnectPhase = "verifying"
)

type ConnectRequest struct {
	Target             Target
	AllowAuthorization bool
	Repository         string
	RunPhase           func(ConnectPhase, func() error) error
}

type ConnectResult struct {
	Provider       string
	Account        RemoteAccount
	BoxName        string
	BoxIdentity    string
	Fingerprint    string
	RemoteKeyID    int64
	RemoteKeyTitle string
	State          State
	Recovered      bool
	Warning        string
}

// AuthorizationState is a local, best-effort read of whether GitHub device
// flow is needed before Connect. It does not call GitHub.
type AuthorizationState struct {
	NeedsDeviceFlow bool
	Account         RemoteAccount
}

type LayerObservation struct {
	State       string
	Fingerprint string
}

type StatusRequest struct {
	Target  Target
	BoxName string
}

type StatusResult struct {
	Provider       string
	BoxName        string
	BoxIdentity    string
	State          StatusState
	Account        RemoteAccount
	Fingerprint    string
	RemoteKeyID    int64
	RemoteKeyTitle string
	Local          LayerObservation
	Box            LayerObservation
	Remote         LayerObservation
	Warnings       []string
}

type DisconnectRequest struct {
	Target             Target
	BoxName            string
	AllowAuthorization bool
}

type DisconnectResult struct {
	Provider        string
	Account         RemoteAccount
	BoxName         string
	BoxIdentity     string
	State           StatusState
	Fingerprint     string
	RemoteKeyID     int64
	RemoteKeyTitle  string
	Local           LayerObservation
	Box             LayerObservation
	Remote          LayerObservation
	Revoked         bool
	BoxFilesRemoved bool
	CleanupPending  bool
	AccountRemoved  bool
	Warning         string
}

type Manager struct {
	store     Store
	secrets   secretstore.Store
	github    GitHubClient
	locks     string
	now       func() time.Time
	mu        sync.Mutex
	ephemeral map[string]Token
}

func NewManager(store Store, secrets secretstore.Store, github GitHubClient, lockDirectory string) (*Manager, error) {
	if store == nil || secrets == nil || github == nil {
		return nil, NewError("internal", "source access dependencies are incomplete", nil)
	}
	if lockDirectory == "" || !filepath.IsAbs(lockDirectory) || filepath.Clean(lockDirectory) != lockDirectory {
		return nil, NewError("internal", "source access lock directory is invalid", nil)
	}
	return &Manager{store: store, secrets: secrets, github: github, locks: lockDirectory, now: time.Now, ephemeral: map[string]Token{}}, nil
}

func (m *Manager) Connect(ctx context.Context, request ConnectRequest) (result ConnectResult, returnErr error) {
	if request.Target == nil || request.Target.BoxIdentity() == "" {
		return ConnectResult{}, NewError("invalid_input", "a Box is required to connect a source", nil)
	}
	lock, err := m.acquireOperation(request.Target.BoxIdentity(), GitHub)
	if err != nil {
		return ConnectResult{}, err
	}
	defer lock.Close()
	boxName := request.Target.BoxName()
	if boxName == "" {
		suffix := request.Target.BoxIdentity()
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		boxName = "box-" + suffix
	}
	if err = m.ensureBoxNameAvailable(ctx, boxName, request.Target.BoxIdentity()); err != nil {
		return ConnectResult{}, err
	}

	_, accountErr := m.store.FindSourceAccount(ctx, GitHub)
	if accountErr != nil && !IsNotFound(accountErr) {
		return ConnectResult{}, accountErr
	}
	newAccount := IsNotFound(accountErr)
	token, account, warning, err := m.resolveAccount(ctx, request.AllowAuthorization)
	if err != nil {
		return ConnectResult{}, err
	}
	bindingCheckpointed := false
	if newAccount {
		defer func() {
			if returnErr == nil || bindingCheckpointed {
				return
			}
			cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancelCleanup()
			if _, cleanupErr := m.cleanupUnboundAccount(cleanupCtx); cleanupErr != nil {
				returnErr = NewError(
					"outcome_unknown",
					"GitHub connection failed before a recoverable Box binding was saved, and the new Source Account could not be removed",
					errors.Join(returnErr, cleanupErr),
				)
			}
		}()
	}
	binding, findErr := m.store.FindBoxSourceIdentity(ctx, request.Target.BoxIdentity(), GitHub)
	recovered := findErr == nil
	if findErr != nil && !IsNotFound(findErr) {
		return ConnectResult{}, findErr
	}
	if findErr == nil && binding.AccountExternalID != "" && binding.AccountExternalID != account.ID {
		return ConnectResult{}, NewError("conflict", "this Box source identity is bound to a different GitHub account", nil)
	}
	if findErr == nil && binding.State == StateConnected && request.Repository != "" {
		preflight, conclusive, preflightErr := m.verifyConnectedRepository(ctx, request.Target, binding, token, account, request.Repository, warning)
		if conclusive {
			return preflight, preflightErr
		}
	}
	var hostIdentity HostIdentity
	if err = runConnectPhase(request.RunPhase, ConnectPhaseCreatingKey, func() error {
		hostKeys, hostErr := m.github.HostKeys(ctx)
		if hostErr != nil {
			return hostErr
		}
		if hostErr = ValidateHostKeys(hostKeys); hostErr != nil {
			return &Error{Code: "conflict", Message: "GitHub host-key metadata is invalid", Context: map[string]string{"reason": "host_key_changed"}, Cause: hostErr}
		}
		hostIdentity, hostErr = request.Target.EnsureSourceIdentity(ctx, EnsureIdentityRequest{Provider: GitHub, HostKeys: hostKeys})
		if hostErr != nil {
			return hostErr
		}
		actualFingerprint, fingerprintErr := PublicKeyFingerprint(hostIdentity.PublicKey)
		if !hostIdentity.Exists || hostIdentity.Provider != GitHub || hostIdentity.Fingerprint == "" || hostIdentity.PublicKey == "" || !hostIdentity.TrustConfigured || !HostFingerprintsMatch(hostIdentity.HostFingerprints, hostKeys) || fingerprintErr != nil || actualFingerprint != hostIdentity.Fingerprint {
			return NewError("outcome_unknown", "the Box did not return a complete GitHub source identity", nil)
		}
		if findErr == nil && binding.Fingerprint != "" && binding.Fingerprint != hostIdentity.Fingerprint {
			return NewError("conflict", "the Box GitHub key differs from the recorded source identity", nil)
		}
		now := m.now().UTC()
		binding.BoxIdentity = request.Target.BoxIdentity()
		binding.BoxName = boxName
		binding.Provider = GitHub
		binding.AccountExternalID = account.ID
		binding.Fingerprint = hostIdentity.Fingerprint
		binding.State = StateConnecting
		binding.UpdatedAt = now
		if binding.CreatedAt.IsZero() {
			binding.CreatedAt = now
		}
		if hostErr = m.store.SaveBoxSourceIdentity(ctx, binding); hostErr != nil {
			return hostErr
		}
		bindingCheckpointed = true
		return nil
	}); err != nil {
		return ConnectResult{}, err
	}

	if err = runConnectPhase(request.RunPhase, ConnectPhaseRegisteringKey, func() error {
		remoteKey, keyErr := m.reconcileKey(ctx, token.AccessToken, binding, hostIdentity.PublicKey)
		if keyErr != nil {
			return keyErr
		}
		binding.RemoteKeyID = remoteKey.ID
		binding.RemoteKeyTitle = remoteKey.Title
		binding.UpdatedAt = m.now().UTC()
		return m.store.SaveBoxSourceIdentity(ctx, binding)
	}); err != nil {
		return ConnectResult{}, err
	}

	if err = runConnectPhase(request.RunPhase, ConnectPhaseVerifying, func() error {
		verification, verifyErr := request.Target.VerifySourceRepository(ctx, VerifyRequest{Provider: GitHub, Repository: request.Repository})
		if verifyErr != nil {
			return verifyErr
		}
		if !verification.Authenticated {
			return NewError("outcome_unknown", "the Box did not confirm GitHub source access", nil)
		}
		binding.State = StateConnected
		binding.UpdatedAt = m.now().UTC()
		return m.store.SaveBoxSourceIdentity(ctx, binding)
	}); err != nil {
		return ConnectResult{}, err
	}
	return ConnectResult{
		Provider: GitHub, Account: account, BoxName: binding.BoxName, BoxIdentity: binding.BoxIdentity,
		Fingerprint: binding.Fingerprint, RemoteKeyID: binding.RemoteKeyID, RemoteKeyTitle: binding.RemoteKeyTitle,
		State: binding.State, Recovered: recovered, Warning: warning,
	}, nil
}

func (m *Manager) verifyConnectedRepository(ctx context.Context, target Target, binding BoxIdentity, token Token, account RemoteAccount, repository, warning string) (ConnectResult, bool, error) {
	hostIdentity, err := target.InspectSourceIdentity(ctx, GitHub)
	if err != nil || !hostIdentity.Exists || hostIdentity.Provider != GitHub || !hostIdentity.TrustConfigured || hostIdentity.Fingerprint != binding.Fingerprint {
		return ConnectResult{}, false, nil
	}
	hostKeys, err := m.github.HostKeys(ctx)
	if err != nil {
		return ConnectResult{}, true, err
	}
	if !HostFingerprintsMatch(hostIdentity.HostFingerprints, hostKeys) {
		return ConnectResult{}, false, nil
	}
	actualFingerprint, fingerprintErr := PublicKeyFingerprint(hostIdentity.PublicKey)
	if fingerprintErr != nil || actualFingerprint != binding.Fingerprint {
		return ConnectResult{}, false, nil
	}
	keys, err := m.github.ListKeys(ctx, token.AccessToken)
	if err != nil {
		return ConnectResult{}, true, err
	}
	remote, present, mismatch := findRemoteKey(keys, binding)
	if mismatch {
		return ConnectResult{}, true, NewError("conflict", "the recorded GitHub key ID now has a different fingerprint", nil)
	}
	if !present {
		return ConnectResult{}, false, nil
	}
	verification, err := target.VerifySourceRepository(ctx, VerifyRequest{Provider: GitHub, Repository: repository})
	if err != nil {
		return ConnectResult{}, true, err
	}
	if !verification.Authenticated {
		return ConnectResult{}, true, NewError("outcome_unknown", "the Box did not confirm GitHub source access", nil)
	}
	return ConnectResult{
		Provider: GitHub, Account: account, BoxName: binding.BoxName, BoxIdentity: binding.BoxIdentity,
		Fingerprint: binding.Fingerprint, RemoteKeyID: remote.ID, RemoteKeyTitle: remote.Title,
		State: StateConnected, Recovered: true, Warning: warning,
	}, true, nil
}

func (m *Manager) Status(ctx context.Context, request StatusRequest) (StatusResult, error) {
	binding, target, err := m.resolveBinding(ctx, request.Target, request.BoxName)
	if IsNotFound(err) {
		return StatusResult{Provider: GitHub, BoxName: request.BoxName, State: StatusNotConnected, Local: LayerObservation{State: "absent"}, Box: LayerObservation{State: "unknown"}, Remote: LayerObservation{State: "unknown"}, Warnings: []string{}}, nil
	}
	if err != nil {
		return StatusResult{}, err
	}
	lock, err := m.acquireOperation(binding.BoxIdentity, GitHub)
	if err != nil {
		return StatusResult{}, err
	}
	defer lock.Close()

	result := StatusResult{
		Provider: GitHub, BoxName: binding.BoxName, BoxIdentity: binding.BoxIdentity,
		Fingerprint: binding.Fingerprint, RemoteKeyID: binding.RemoteKeyID, RemoteKeyTitle: binding.RemoteKeyTitle,
		State: StatusUnknown, Local: LayerObservation{State: string(binding.State), Fingerprint: binding.Fingerprint},
		Box: LayerObservation{State: "unknown"}, Remote: LayerObservation{State: "unknown"}, Warnings: []string{},
	}
	if account, accountErr := m.store.FindSourceAccount(ctx, GitHub); accountErr == nil {
		remote := RemoteAccount{ID: account.ExternalID, Login: account.Login}
		if !validRemoteAccount(remote) {
			result.State = StatusConflict
			result.Warnings = append(result.Warnings, "Stored GitHub account metadata is invalid.")
			return result, nil
		}
		result.Account = remote
	} else if !IsNotFound(accountErr) {
		return StatusResult{}, accountErr
	}
	if binding.State == StateCleanupPending {
		return m.resumeCleanup(ctx, target, binding, result)
	}

	var inspectedIdentity HostIdentity
	if target != nil {
		hostIdentity, inspectErr := target.InspectSourceIdentity(ctx, GitHub)
		if inspectErr != nil {
			result.Warnings = append(result.Warnings, "Box source identity could not be inspected: "+inspectErr.Error())
		} else if !hostIdentity.Exists {
			result.Box.State = "absent"
		} else if !hostIdentity.TrustConfigured {
			result.Box = LayerObservation{State: "trust_missing", Fingerprint: hostIdentity.Fingerprint}
		} else {
			inspectedIdentity = hostIdentity
			result.Box = LayerObservation{State: "present", Fingerprint: hostIdentity.Fingerprint}
		}
	}

	token, account, credentialWarning, tokenErr := m.resolveAccount(ctx, false)
	if tokenErr != nil {
		if ErrorCode(tokenErr) == "authentication_required" {
			result.State = StatusActionRequired
			result.Warnings = append(result.Warnings, tokenErr.Error())
			return result, nil
		}
		if ErrorCode(tokenErr) == CodeSourceUnavailable {
			result.State = StatusUnknown
			result.Warnings = append(result.Warnings, tokenErr.Error())
			return result, nil
		}
		return StatusResult{}, tokenErr
	}
	if credentialWarning != "" {
		result.Warnings = append(result.Warnings, credentialWarning)
	}
	result.Account = account
	if account.ID != binding.AccountExternalID {
		result.State = StatusConflict
		result.Warnings = append(result.Warnings, "The local Source Account does not match this Box binding.")
		return result, nil
	}
	keys, listErr := m.github.ListKeys(ctx, token.AccessToken)
	if listErr != nil {
		if ErrorCode(listErr) == CodeSourceUnavailable {
			result.State = StatusUnknown
			result.Warnings = append(result.Warnings, "GitHub key state is unavailable: "+listErr.Error())
			return result, nil
		}
		if ErrorCode(listErr) == "authentication_required" {
			result.State = StatusActionRequired
			result.Warnings = append(result.Warnings, listErr.Error())
			return result, nil
		}
		return StatusResult{}, listErr
	}
	remote, present, mismatch := findRemoteKey(keys, binding)
	if present {
		result.Remote = LayerObservation{State: "present", Fingerprint: remote.Fingerprint}
		result.RemoteKeyID = remote.ID
		result.RemoteKeyTitle = remote.Title
	} else {
		result.Remote.State = "absent"
	}
	if result.Box.State == "present" {
		hostKeys, hostKeysErr := m.github.HostKeys(ctx)
		if hostKeysErr != nil {
			result.State = StatusUnknown
			result.Warnings = append(result.Warnings, "GitHub host-key metadata is unavailable: "+hostKeysErr.Error())
			return result, nil
		}
		if !HostFingerprintsMatch(inspectedIdentity.HostFingerprints, hostKeys) {
			result.Box.State = "trust_changed"
		}
	}
	if binding.State == StateDisconnecting {
		if mismatch {
			result.State = StatusConflict
			return result, nil
		}
		if present {
			result.State = StatusActionRequired
			result.Warnings = append(result.Warnings, "GitHub key revocation was interrupted; run source disconnect again.")
			return result, nil
		}
		binding.State = StateCleanupPending
		binding.UpdatedAt = m.now().UTC()
		if err = m.store.SaveBoxSourceIdentity(ctx, binding); err != nil {
			return StatusResult{}, err
		}
		result.Local.State = string(StateCleanupPending)
		return m.resumeCleanup(ctx, target, binding, result)
	}
	if binding.State == StateConnecting {
		if mismatch || result.Box.State == "trust_changed" || (result.Box.State == "present" && result.Box.Fingerprint != binding.Fingerprint) {
			result.State = StatusConflict
			return result, nil
		}
		if !present || result.Box.State == "absent" || result.Box.State == "trust_missing" {
			result.State = StatusActionRequired
			result.Warnings = append(result.Warnings, "GitHub source connection was interrupted; run source connect again.")
			return result, nil
		}
		if result.Box.State == "unknown" {
			result.State = StatusUnknown
			return result, nil
		}
		if binding.RemoteKeyID != remote.ID || binding.RemoteKeyTitle != remote.Title {
			binding.RemoteKeyID = remote.ID
			binding.RemoteKeyTitle = remote.Title
			binding.UpdatedAt = m.now().UTC()
			if err = m.store.SaveBoxSourceIdentity(ctx, binding); err != nil {
				return StatusResult{}, err
			}
			result.RemoteKeyID = remote.ID
			result.RemoteKeyTitle = remote.Title
		}
		result.State = StatusActionRequired
		result.Warnings = append(result.Warnings, "GitHub key registration is present, but SSH verification is pending; run source connect again.")
		return result, nil
	}
	if mismatch || (result.Box.State == "present" && result.Box.Fingerprint != binding.Fingerprint) || (result.Box.State != "present" && result.Box.State != "unknown") || !present {
		result.State = StatusConflict
		return result, nil
	}
	if result.Box.State == "unknown" {
		result.State = StatusUnknown
		return result, nil
	}
	result.State = StatusConnected
	return result, nil
}

func (m *Manager) resumeCleanup(ctx context.Context, target Target, binding BoxIdentity, result StatusResult) (StatusResult, error) {
	result.State = StatusCleanupPending
	result.Remote.State = "revoked"
	if target == nil {
		return result, nil
	}
	removed, err := target.RemoveSourceIdentity(ctx, RemoveIdentityRequest{Provider: GitHub, ExpectedFingerprint: binding.Fingerprint})
	if err != nil {
		result.Warnings = append(result.Warnings, "GitHub access is revoked, but Box key cleanup is still pending: "+SafeWarning(err.Error()))
		return result, nil
	}
	if !removed.Removed {
		result.Warnings = append(result.Warnings, "GitHub access is revoked, but the Box did not confirm key cleanup.")
		return result, nil
	}
	if _, err = m.finishCleanup(ctx, binding); err != nil {
		result.Box = LayerObservation{State: "absent"}
		result.Warnings = append(result.Warnings, "GitHub access is revoked and the Box key is removed, but local Source Account cleanup is still pending.")
		return result, nil
	}
	result.State = StatusNotConnected
	result.Local = LayerObservation{State: "absent"}
	result.Box = LayerObservation{State: "absent"}
	result.Account = RemoteAccount{}
	result.Warnings = append(result.Warnings, "Completed previously pending Box key cleanup.")
	return result, nil
}

func (m *Manager) Disconnect(ctx context.Context, request DisconnectRequest) (DisconnectResult, error) {
	binding, target, err := m.resolveBinding(ctx, request.Target, request.BoxName)
	if IsNotFound(err) {
		operationIdentity := request.BoxName
		if target != nil && target.BoxIdentity() != "" {
			operationIdentity = target.BoxIdentity()
		}
		lock, lockErr := m.acquireOperation(operationIdentity, GitHub)
		if lockErr != nil {
			return DisconnectResult{}, lockErr
		}
		defer lock.Close()
		accountRemoved, cleanupErr := m.cleanupUnboundAccount(ctx)
		if cleanupErr != nil {
			return DisconnectResult{}, cleanupErr
		}
		return DisconnectResult{Provider: GitHub, BoxName: request.BoxName, State: StatusNotConnected, Local: LayerObservation{State: "absent"}, Box: LayerObservation{State: "unknown"}, Remote: LayerObservation{State: "unknown"}, Revoked: true, AccountRemoved: accountRemoved}, nil
	}
	if err != nil {
		return DisconnectResult{}, err
	}
	lock, err := m.acquireOperation(binding.BoxIdentity, GitHub)
	if err != nil {
		return DisconnectResult{}, err
	}
	defer lock.Close()
	result := DisconnectResult{
		Provider: GitHub, BoxName: binding.BoxName, BoxIdentity: binding.BoxIdentity,
		State: StatusUnknown, Fingerprint: binding.Fingerprint, RemoteKeyID: binding.RemoteKeyID, RemoteKeyTitle: binding.RemoteKeyTitle,
		Local: LayerObservation{State: string(binding.State), Fingerprint: binding.Fingerprint},
		Box:   LayerObservation{State: "unknown"}, Remote: LayerObservation{State: "unknown"},
	}
	if account, accountErr := m.store.FindSourceAccount(ctx, GitHub); accountErr == nil {
		remote := RemoteAccount{ID: account.ExternalID, Login: account.Login}
		if !validRemoteAccount(remote) {
			return DisconnectResult{}, NewError("conflict", "stored GitHub account metadata is invalid", nil)
		}
		result.Account = remote
	} else if !IsNotFound(accountErr) {
		return DisconnectResult{}, accountErr
	}

	if binding.State != StateCleanupPending {
		token, account, credentialWarning, tokenErr := m.resolveAccount(ctx, request.AllowAuthorization)
		if tokenErr != nil {
			return DisconnectResult{}, tokenErr
		}
		result.Warning = appendWarning(result.Warning, credentialWarning)
		result.Account = account
		keys, listErr := m.github.ListKeys(ctx, token.AccessToken)
		if listErr != nil {
			return DisconnectResult{}, listErr
		}
		remote, present, mismatch := findRemoteKey(keys, binding)
		if mismatch {
			return DisconnectResult{}, NewError("conflict", "the recorded GitHub key ID now has a different fingerprint", nil)
		}
		binding.State = StateDisconnecting
		binding.UpdatedAt = m.now().UTC()
		if err = m.store.SaveBoxSourceIdentity(ctx, binding); err != nil {
			return DisconnectResult{}, err
		}
		if present {
			_, deleteErr := m.github.DeleteKey(ctx, token.AccessToken, remote.ID)
			if deleteErr != nil {
				return DisconnectResult{}, deleteErr
			}
			// A false result means GitHub observed the key already absent. Either
			// successful response still establishes the required revoked state.
			result.Revoked = true
		} else {
			result.Revoked = true
		}
		binding.State = StateCleanupPending
		binding.UpdatedAt = m.now().UTC()
		if err = m.store.SaveBoxSourceIdentity(ctx, binding); err != nil {
			return DisconnectResult{}, NewError("outcome_unknown", "GitHub access was revoked but local cleanup could not be checkpointed", err)
		}
		result.Local.State = string(StateCleanupPending)
		result.Remote.State = "revoked"
	} else {
		result.Revoked = true
		result.State = StatusCleanupPending
		result.Remote.State = "revoked"
	}

	if target == nil {
		result.State = StatusCleanupPending
		result.CleanupPending = true
		result.Warning = appendWarning(result.Warning, "GitHub access is revoked; re-adopt the Box to remove its now-inactive private key.")
		return result, nil
	}
	removed, removeErr := target.RemoveSourceIdentity(ctx, RemoveIdentityRequest{Provider: GitHub, ExpectedFingerprint: binding.Fingerprint})
	if removeErr != nil {
		result.State = StatusCleanupPending
		result.CleanupPending = true
		result.Warning = appendWarning(result.Warning, "GitHub access is revoked, but Box key cleanup is pending: "+SafeWarning(removeErr.Error()))
		return result, nil
	}
	if !removed.Removed {
		result.State = StatusCleanupPending
		result.CleanupPending = true
		result.Warning = appendWarning(result.Warning, "GitHub access is revoked, but the Box did not confirm key cleanup.")
		return result, nil
	}
	result.BoxFilesRemoved = removed.Removed
	result.Box.State = "absent"
	result.AccountRemoved, err = m.finishCleanup(ctx, binding)
	if err != nil {
		result.State = StatusCleanupPending
		result.CleanupPending = true
		result.Warning = appendWarning(result.Warning, "GitHub access is revoked and the Box key is removed, but local Source Account cleanup is still pending.")
		return result, nil
	}
	result.State = StatusNotConnected
	result.Local.State = "absent"
	return result, nil
}

func (m *Manager) reconcileKey(ctx context.Context, accessToken string, binding BoxIdentity, publicKey string) (RemoteKey, error) {
	keys, err := m.github.ListKeys(ctx, accessToken)
	if err != nil {
		return RemoteKey{}, err
	}
	if remote, present, mismatch := findRemoteKey(keys, binding); mismatch {
		return RemoteKey{}, NewError("conflict", "the recorded GitHub key ID now has a different fingerprint", nil)
	} else if present {
		return remote, nil
	}
	for _, key := range keys {
		if key.Fingerprint == binding.Fingerprint {
			return key, nil
		}
	}
	title := "Schooner / " + binding.BoxName
	created, createErr := m.github.CreateKey(ctx, accessToken, title, publicKey)
	if createErr == nil {
		if created.Fingerprint != binding.Fingerprint {
			return RemoteKey{}, NewError("outcome_unknown", "GitHub returned a different key fingerprint after registration", nil)
		}
		return created, nil
	}
	// A timeout or 422 can follow a successful create. Re-list exactly once and
	// recover by fingerprint before surfacing the original failure.
	keys, listErr := m.github.ListKeys(ctx, accessToken)
	if listErr == nil {
		for _, key := range keys {
			if key.Fingerprint == binding.Fingerprint {
				return key, nil
			}
		}
	}
	return RemoteKey{}, createErr
}

func findRemoteKey(keys []RemoteKey, binding BoxIdentity) (RemoteKey, bool, bool) {
	if binding.RemoteKeyID != 0 {
		for _, key := range keys {
			if key.ID == binding.RemoteKeyID {
				return key, key.Fingerprint == binding.Fingerprint, key.Fingerprint != binding.Fingerprint
			}
		}
	}
	for _, key := range keys {
		if key.Fingerprint == binding.Fingerprint {
			return key, true, false
		}
	}
	return RemoteKey{}, false, false
}

func (m *Manager) resolveBinding(ctx context.Context, target Target, boxName string) (BoxIdentity, Target, error) {
	resolvedName := boxName
	if resolvedName == "" && target != nil {
		resolvedName = target.BoxName()
	}
	if target != nil && target.BoxIdentity() != "" {
		binding, identityErr := m.store.FindBoxSourceIdentity(ctx, target.BoxIdentity(), GitHub)
		if identityErr == nil {
			if resolvedName == "" {
				return binding, target, nil
			}
			named, nameErr := m.store.FindBoxSourceIdentityByName(ctx, resolvedName, GitHub)
			if IsNotFound(nameErr) {
				return binding, target, nil
			}
			if nameErr != nil {
				return BoxIdentity{}, nil, nameErr
			}
			if named.BoxIdentity != binding.BoxIdentity {
				return BoxIdentity{}, nil, NewError("conflict", "the Box name refers to a different retained source identity", nil)
			}
			return binding, target, nil
		}
		if !IsNotFound(identityErr) || resolvedName == "" {
			return BoxIdentity{}, nil, identityErr
		}
		binding, nameErr := m.store.FindBoxSourceIdentityByName(ctx, resolvedName, GitHub)
		if nameErr == nil {
			// The inventory name was reused by another machine. The retained
			// binding remains authoritative, but that new target must never be
			// used to inspect or remove the former machine's files.
			return binding, nil, nil
		}
		if IsNotFound(nameErr) {
			return BoxIdentity{}, nil, identityErr
		}
		return BoxIdentity{}, nil, nameErr
	}
	if resolvedName == "" {
		return BoxIdentity{}, nil, NewError("invalid_input", "a Box is required", nil)
	}
	binding, err := m.store.FindBoxSourceIdentityByName(ctx, resolvedName, GitHub)
	return binding, nil, err
}

func (m *Manager) ensureBoxNameAvailable(ctx context.Context, boxName, boxIdentity string) error {
	binding, err := m.store.FindBoxSourceIdentityByName(ctx, boxName, GitHub)
	if IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if binding.BoxIdentity != boxIdentity {
		return NewError("conflict", "disconnect the retained source identity using this Box name before connecting its replacement", nil)
	}
	return nil
}

func (m *Manager) resolveAccount(ctx context.Context, allowAuthorization bool) (Token, RemoteAccount, string, error) {
	account, err := m.store.FindSourceAccount(ctx, GitHub)
	if err != nil && !IsNotFound(err) {
		return Token{}, RemoteAccount{}, "", err
	}
	if IsNotFound(err) {
		if !allowAuthorization {
			return Token{}, RemoteAccount{}, "", authenticationRequired("GitHub authorization is required")
		}
		return m.authorize(ctx, Account{})
	}
	token, persisted, tokenErr := m.loadToken(account)
	if tokenErr != nil {
		if ErrorCode(tokenErr) != "authentication_required" || !allowAuthorization {
			return Token{}, RemoteAccount{}, "", tokenErr
		}
		return m.authorize(ctx, account)
	}
	if token.AccessToken == "" {
		if !allowAuthorization {
			return Token{}, RemoteAccount{}, "", authenticationRequired("the GitHub Source Account needs to be reconnected")
		}
		return m.authorize(ctx, account)
	}
	warning := ""
	if account.Status == "action_required" && persisted {
		account.Status = "connected"
		account.UpdatedAt = m.now().UTC()
		if err = m.store.SaveSourceAccount(ctx, account); err != nil {
			return Token{}, RemoteAccount{}, "", err
		}
		warning = "Recovered an interrupted operating-system credential-store checkpoint."
	}
	if token.AccessExpiresAt.IsZero() || token.AccessExpiresAt.After(m.now().UTC().Add(30*time.Second)) {
		remote := RemoteAccount{ID: account.ExternalID, Login: account.Login}
		if !validRemoteAccount(remote) {
			return Token{}, RemoteAccount{}, "", NewError("conflict", "stored GitHub account metadata is invalid", nil)
		}
		if allowAuthorization {
			verified, verifyErr := m.github.Account(ctx, token.AccessToken)
			if verifyErr != nil {
				if ErrorCode(verifyErr) == "authentication_required" {
					return m.authorize(ctx, account)
				}
				return Token{}, RemoteAccount{}, "", verifyErr
			}
			if !validRemoteAccount(verified) || verified.ID != account.ExternalID {
				return Token{}, RemoteAccount{}, "", NewError("conflict", "GitHub authorization now resolves to a different account", nil)
			}
			remote = verified
		}
		return token, remote, warning, nil
	}
	if token.RefreshToken != "" && (token.RefreshExpiresAt.IsZero() || token.RefreshExpiresAt.After(m.now().UTC())) {
		refreshed, refreshErr := m.github.Refresh(ctx, token.RefreshToken)
		if refreshErr == nil {
			if !validCredentialToken(refreshed) {
				return Token{}, RemoteAccount{}, "", authenticationRequired("GitHub returned an incomplete refreshed authorization")
			}
			remote, identifyErr := m.github.Account(ctx, refreshed.AccessToken)
			if identifyErr != nil {
				return Token{}, RemoteAccount{}, "", identifyErr
			}
			if remote.ID != account.ExternalID {
				return Token{}, RemoteAccount{}, "", NewError("conflict", "GitHub authorization now resolves to a different account", nil)
			}
			persistWarning, persistErr := m.persistToken(ctx, account, refreshed, remote)
			return refreshed, remote, appendWarning(warning, persistWarning), persistErr
		}
		if ErrorCode(refreshErr) != "authentication_required" {
			return Token{}, RemoteAccount{}, "", refreshErr
		}
	}
	if !allowAuthorization {
		return Token{}, RemoteAccount{}, "", authenticationRequired("the GitHub Source Account needs to be reauthorized")
	}
	return m.authorize(ctx, account)
}

func (m *Manager) authorize(ctx context.Context, existing Account) (Token, RemoteAccount, string, error) {
	token, err := m.github.Authorize(ctx)
	if err != nil {
		return Token{}, RemoteAccount{}, "", err
	}
	if !validCredentialToken(token) {
		return Token{}, RemoteAccount{}, "", authenticationRequired("GitHub returned an incomplete authorization")
	}
	remote, err := m.github.Account(ctx, token.AccessToken)
	if err != nil {
		return Token{}, RemoteAccount{}, "", err
	}
	bindings, listErr := m.store.ListBoxSourceIdentities(ctx, GitHub)
	if listErr != nil {
		return Token{}, RemoteAccount{}, "", listErr
	}
	for _, binding := range bindings {
		if binding.AccountExternalID != remote.ID {
			return Token{}, RemoteAccount{}, "", NewError("conflict", "disconnect every Box before authorizing a different GitHub account", nil)
		}
	}
	if existing.ExternalID != "" && existing.ExternalID != remote.ID && len(bindings) > 0 {
		return Token{}, RemoteAccount{}, "", NewError("conflict", "disconnect every Box before authorizing a different GitHub account", nil)
	}
	warning, err := m.persistToken(ctx, existing, token, remote)
	return token, remote, warning, err
}

func (m *Manager) loadToken(account Account) (Token, bool, error) {
	m.mu.Lock()
	ephemeral, ok := m.ephemeral[GitHub]
	m.mu.Unlock()
	if ok {
		return ephemeral, false, nil
	}
	if (account.Status != "connected" && account.Status != "action_required") || account.CredentialKey == "" || account.CredentialGeneration == "" {
		return Token{}, false, nil
	}
	value, err := m.secrets.Get(account.CredentialKey)
	if err != nil {
		return Token{}, false, NewError(CodeSourceUnavailable, "operating-system credential storage is unavailable", err)
	}
	if value == "" {
		return Token{}, false, nil
	}
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
		Generation    string `json:"generation"`
		Token
	}
	if err = json.Unmarshal([]byte(value), &envelope); err != nil || envelope.SchemaVersion != "1" || envelope.Generation != account.CredentialGeneration || !validCredentialToken(envelope.Token) {
		return Token{}, false, authenticationRequired("the stored GitHub credential is invalid")
	}
	return envelope.Token, true, nil
}

func (m *Manager) persistToken(ctx context.Context, existing Account, token Token, remote RemoteAccount) (string, error) {
	if !validCredentialToken(token) || !validRemoteAccount(remote) {
		return "", NewError("authentication_required", "GitHub returned an incomplete authorization", nil)
	}
	generation, err := randomKey()
	if err != nil {
		return "", NewError("internal", "could not create a source credential generation", err)
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion string `json:"schema_version"`
		Generation    string `json:"generation"`
		Token
	}{SchemaVersion: "1", Generation: generation, Token: token})
	if err != nil {
		return "", err
	}
	credentialKey := existing.CredentialKey
	if credentialKey == "" {
		credentialKey, err = randomKey()
		if err != nil {
			return "", NewError("internal", "could not create a source credential reference", err)
		}
	}
	now := m.now().UTC()
	account := existing
	account.Provider, account.ExternalID, account.Login = GitHub, remote.ID, remote.Login
	account.CredentialKey = credentialKey
	account.CredentialGeneration = generation
	account.AccessExpiresAt, account.RefreshExpiresAt = token.AccessExpiresAt, token.RefreshExpiresAt
	// Checkpoint the reference and a non-secret generation before touching the
	// external secret store. A matching envelope can safely finish an interrupted
	// write; an older envelope at the same reference is never trusted.
	account.Status = "action_required"
	account.UpdatedAt = now
	if account.CreatedAt.IsZero() {
		account.CreatedAt = now
	}
	if err = m.store.SaveSourceAccount(ctx, account); err != nil {
		return "", err
	}
	if err = m.secrets.Set(credentialKey, string(encoded)); err != nil {
		m.mu.Lock()
		m.ephemeral[GitHub] = token
		m.mu.Unlock()
		return "Operating-system credential storage is unavailable; GitHub authorization is usable only for this Schooner process.", nil
	}
	account.Status = "connected"
	if err = m.store.SaveSourceAccount(ctx, account); err != nil {
		return "", err
	}
	m.mu.Lock()
	delete(m.ephemeral, GitHub)
	m.mu.Unlock()
	return "", nil
}

func validCredentialToken(token Token) bool {
	for _, value := range []string{token.AccessToken, token.RefreshToken} {
		if value == "" || len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return !token.AccessExpiresAt.IsZero() && !token.RefreshExpiresAt.IsZero()
}

func validRemoteAccount(account RemoteAccount) bool {
	if account.ID == "" || len(account.ID) > 32 || account.Login == "" || len(account.Login) > 39 || account.Login[0] == '-' || account.Login[len(account.Login)-1] == '-' {
		return false
	}
	nonzeroID := false
	for _, character := range account.ID {
		if character < '0' || character > '9' {
			return false
		}
		nonzeroID = nonzeroID || character != '0'
	}
	for _, character := range account.Login {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return nonzeroID
}

func (m *Manager) cleanupUnboundAccount(ctx context.Context) (bool, error) {
	bindings, err := m.store.ListBoxSourceIdentities(ctx, GitHub)
	if err != nil {
		return false, err
	}
	if len(bindings) != 0 {
		return false, nil
	}
	account, err := m.store.FindSourceAccount(ctx, GitHub)
	if err != nil && !IsNotFound(err) {
		return false, err
	}
	if IsNotFound(err) {
		m.mu.Lock()
		delete(m.ephemeral, GitHub)
		m.mu.Unlock()
		return false, nil
	}
	if account.CredentialKey != "" {
		if err = m.secrets.Delete(account.CredentialKey); err != nil {
			return false, NewError("internal", "could not remove the unbound GitHub credential from operating-system storage", err)
		}
	}
	m.mu.Lock()
	delete(m.ephemeral, GitHub)
	m.mu.Unlock()
	if err = m.store.DeleteSourceAccount(ctx, GitHub); err != nil {
		return false, err
	}
	return true, nil
}

func (m *Manager) finishCleanup(ctx context.Context, binding BoxIdentity) (bool, error) {
	bindings, err := m.store.ListBoxSourceIdentities(ctx, GitHub)
	if err != nil {
		return false, err
	}
	last := len(bindings) == 1 && bindings[0].BoxIdentity == binding.BoxIdentity
	if !last {
		if err = m.store.DeleteBoxSourceIdentity(ctx, binding.BoxIdentity, GitHub); err != nil {
			return false, err
		}
		return false, nil
	}
	account, err := m.store.FindSourceAccount(ctx, GitHub)
	if err != nil && !IsNotFound(err) {
		return false, err
	}
	if err == nil && account.CredentialKey != "" {
		if err = m.secrets.Delete(account.CredentialKey); err != nil {
			return false, NewError("internal", "could not remove the GitHub credential from operating-system storage", err)
		}
	}
	m.mu.Lock()
	delete(m.ephemeral, GitHub)
	m.mu.Unlock()
	if err == nil {
		if err = m.store.DeleteSourceAccount(ctx, GitHub); err != nil {
			return false, err
		}
	}
	if err = m.store.DeleteBoxSourceIdentity(ctx, binding.BoxIdentity, GitHub); err != nil {
		return false, err
	}
	return true, nil
}

// HasBinding reports only persisted local source metadata. It never inspects a
// Box or calls GitHub, which lets Box removal preserve its existing authority
// boundary while still warning about separately managed source access.
func (m *Manager) HasBinding(ctx context.Context, boxIdentity string) (bool, error) {
	if boxIdentity == "" {
		return false, nil
	}
	_, err := m.store.FindBoxSourceIdentity(ctx, boxIdentity, GitHub)
	if IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// AuthorizationState reports whether Connect will need GitHub device flow
// based on local Source Account and credential-store state.
func (m *Manager) AuthorizationState(ctx context.Context) (AuthorizationState, error) {
	account, err := m.store.FindSourceAccount(ctx, GitHub)
	if IsNotFound(err) {
		return AuthorizationState{NeedsDeviceFlow: true}, nil
	}
	if err != nil {
		return AuthorizationState{}, err
	}
	remote := RemoteAccount{ID: account.ExternalID, Login: account.Login}
	token, _, tokenErr := m.loadToken(account)
	if tokenErr != nil {
		if ErrorCode(tokenErr) == "authentication_required" {
			return AuthorizationState{NeedsDeviceFlow: true, Account: remote}, nil
		}
		return AuthorizationState{}, tokenErr
	}
	if token.AccessToken == "" {
		return AuthorizationState{NeedsDeviceFlow: true, Account: remote}, nil
	}
	fresh := token.AccessExpiresAt.IsZero() || token.AccessExpiresAt.After(m.now().UTC().Add(30*time.Second))
	refreshable := token.RefreshToken != "" && (token.RefreshExpiresAt.IsZero() || token.RefreshExpiresAt.After(m.now().UTC()))
	if !fresh && !refreshable {
		return AuthorizationState{NeedsDeviceFlow: true, Account: remote}, nil
	}
	return AuthorizationState{NeedsDeviceFlow: false, Account: remote}, nil
}

func (m *Manager) BindingCount(ctx context.Context) (int, error) {
	bindings, err := m.store.ListBoxSourceIdentities(ctx, GitHub)
	if err != nil {
		return 0, err
	}
	return len(bindings), nil
}

func runConnectPhase(runner func(ConnectPhase, func() error) error, phase ConnectPhase, fn func() error) error {
	if runner == nil {
		return fn()
	}
	return runner(phase, fn)
}

func authenticationRequired(message string) error {
	return &Error{Code: "authentication_required", Message: message, Context: map[string]string{"reason": "credentials_missing"}}
}

func randomKey() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

type fileLock struct{ file *os.File }

type operationLocks struct {
	account *fileLock
	box     *fileLock
}

func (m *Manager) acquireOperation(boxIdentity, provider string) (*operationLocks, error) {
	// The provider lock protects the one shared Source Account and final-account
	// cleanup. The Box lock separately preserves the required per-Box/provider
	// mutation boundary. Every operation acquires them in this fixed order.
	account, err := m.acquire("source-account", provider)
	if err != nil {
		if ErrorCode(err) == "operation_in_progress" {
			return nil, NewError("operation_in_progress", "another GitHub Source Account operation is already running", err)
		}
		return nil, err
	}
	boxLock, err := m.acquire(boxIdentity, provider)
	if err != nil {
		account.Close()
		return nil, err
	}
	return &operationLocks{account: account, box: boxLock}, nil
}

func (l *operationLocks) Close() {
	if l == nil {
		return
	}
	if l.box != nil {
		l.box.Close()
	}
	if l.account != nil {
		l.account.Close()
	}
}

func (m *Manager) acquire(boxIdentity, provider string) (*fileLock, error) {
	if err := os.MkdirAll(m.locks, 0o700); err != nil {
		return nil, NewError("outcome_unknown", "source operation locking is unavailable", err)
	}
	info, err := os.Lstat(m.locks)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, NewError("conflict", "source operation lock directory is invalid", err)
	}
	if err = os.Chmod(m.locks, 0o700); err != nil {
		return nil, NewError("outcome_unknown", "source operation locking is unavailable", err)
	}
	digest := sha256.Sum256([]byte(boxIdentity + "\x00" + provider))
	path := filepath.Join(m.locks, hex.EncodeToString(digest[:])+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, NewError("outcome_unknown", "source operation locking is unavailable", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, NewError("operation_in_progress", "another source operation is already using this Box", err)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) Close() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}

// SortedHostKeys returns a deterministic defensive copy for protocol and file
// generation adapters.
func SortedHostKeys(values []HostKey) []HostKey {
	result := append([]HostKey(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Fingerprint == result[j].Fingerprint {
			return result[i].Key < result[j].Key
		}
		return result[i].Fingerprint < result[j].Fingerprint
	})
	return result
}

func SafeWarning(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func appendWarning(existing, next string) string {
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + " " + next
}

func IsUnavailable(err error) bool { return ErrorCode(err) == CodeSourceUnavailable }

func IsAuthenticationRequired(err error) bool {
	return ErrorCode(err) == "authentication_required" || errors.Is(err, os.ErrPermission)
}
