package source

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f"
	testHostKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICAhIiMkJSYnKCkqKywtLi8wMTIzNDU2Nzg5Ojs8PT4/"
)

func TestConnectReconcilesOneBoxKeyAndReusesLocalAccount(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	first, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.Account.Login != "octocat" || first.State != StateConnected || first.RemoteKeyID == 0 || first.Fingerprint != target.identity.Fingerprint {
		t.Fatalf("result=%+v", first)
	}
	if github.authorizeCalls != 1 || github.createCalls != 1 || github.verifyCreateTitle != "Schooner / work" {
		t.Fatalf("authorize=%d create=%d title=%q", github.authorizeCalls, github.createCalls, github.verifyCreateTitle)
	}
	if store.account.CredentialKey == "" || store.account.CredentialGeneration == "" || strings.Contains(store.account.CredentialKey, "token-one") || len(secrets.values) != 1 {
		t.Fatalf("account=%+v stored secret count=%d", store.account, len(secrets.values))
	}
	if binding := store.identities[target.boxIdentity]; binding.State != StateConnected || binding.Fingerprint != target.identity.Fingerprint {
		t.Fatalf("binding=%+v", binding)
	}

	second, err := manager.Connect(t.Context(), ConnectRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Recovered || github.authorizeCalls != 1 || github.createCalls != 1 {
		t.Fatalf("second=%+v authorize=%d create=%d", second, github.authorizeCalls, github.createCalls)
	}
}

func TestAuthorizationStateAndBindingCountFollowConnect(t *testing.T) {
	manager, _, _, _, target := testManager(t)
	state, err := manager.AuthorizationState(t.Context())
	if err != nil || !state.NeedsDeviceFlow || state.Account.Login != "" {
		t.Fatalf("before connect: state=%+v err=%v", state, err)
	}
	count, err := manager.BindingCount(t.Context())
	if err != nil || count != 0 {
		t.Fatalf("before connect: count=%d err=%v", count, err)
	}

	var phases []ConnectPhase
	if _, err = manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true, RunPhase: func(phase ConnectPhase, fn func() error) error {
		phases = append(phases, phase)
		return fn()
	}}); err != nil {
		t.Fatal(err)
	}
	if len(phases) != 3 || phases[0] != ConnectPhaseCreatingKey || phases[1] != ConnectPhaseRegisteringKey || phases[2] != ConnectPhaseVerifying {
		t.Fatalf("phases=%v", phases)
	}

	state, err = manager.AuthorizationState(t.Context())
	if err != nil || state.NeedsDeviceFlow || state.Account.Login != "octocat" {
		t.Fatalf("after connect: state=%+v err=%v", state, err)
	}
	count, err = manager.BindingCount(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("after connect: count=%d err=%v", count, err)
	}
}

func TestUnavailableKeyringUsesInvocationMemoryOnly(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	secrets.setErr = errors.New("locked")
	result, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Warning == "" || store.account.CredentialKey == "" || store.account.Status != "action_required" || len(secrets.values) != 0 {
		t.Fatalf("result=%+v account=%+v", result, store.account)
	}
	if _, err = manager.Connect(t.Context(), ConnectRequest{Target: target}); err != nil {
		t.Fatalf("ephemeral credential was not reusable: %v", err)
	}
	if github.authorizeCalls != 1 {
		t.Fatalf("authorize calls=%d", github.authorizeCalls)
	}
}

func TestCredentialStoreReadFailureNeverReauthorizes(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	generation := store.account.CredentialGeneration
	secrets.getErr = errors.New("keyring locked")

	_, _, _, err := manager.resolveAccount(t.Context(), true, nil)
	if ErrorCode(err) != CodeSourceUnavailable || github.authorizeCalls != 1 || store.account.CredentialGeneration != generation {
		t.Fatalf("err=%v authorizeCalls=%d account=%+v", err, github.authorizeCalls, store.account)
	}
	status, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil || status.State != StatusUnknown || len(status.Warnings) == 0 || github.authorizeCalls != 1 {
		t.Fatalf("status=%+v authorizeCalls=%d err=%v", status, github.authorizeCalls, err)
	}
}

func TestInteractiveConnectReauthorizesARejectedUnexpiredToken(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	previousGeneration := store.account.CredentialGeneration
	github.accountErrOnce = authenticationRequired("GitHub rejected the access token")
	confirmations := 0

	result, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true, BeforeAuthorization: func(_ context.Context, account RemoteAccount) error {
		confirmations++
		if account.ID != "42" || account.Login != "octocat" {
			t.Fatalf("account=%+v", account)
		}
		return nil
	}})
	if err != nil || result.State != StateConnected || github.authorizeCalls != 2 || confirmations != 1 || store.account.CredentialGeneration == previousGeneration {
		t.Fatalf("result=%+v authorizeCalls=%d confirmations=%d account=%+v err=%v", result, github.authorizeCalls, confirmations, store.account, err)
	}
}

func TestInteractiveConnectDoesNotReauthorizeWhenConfirmationFails(t *testing.T) {
	manager, _, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	github.accountErrOnce = authenticationRequired("GitHub rejected the access token")
	declined := errors.New("authorization not confirmed")

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true, BeforeAuthorization: func(context.Context, RemoteAccount) error {
		return declined
	}})
	if !errors.Is(err, declined) || github.authorizeCalls != 1 {
		t.Fatalf("err=%v authorizeCalls=%d", err, github.authorizeCalls)
	}
}

func TestConnectRemovesNewAuthorizationWhenNoBindingCanBeCheckpointed(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	github.hostKeysErr = NewError(CodeSourceUnavailable, "GitHub metadata unavailable", nil)

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if ErrorCode(err) != CodeSourceUnavailable {
		t.Fatalf("err=%v", err)
	}
	if store.account.Provider != "" || len(store.identities) != 0 || len(secrets.values) != 0 {
		t.Fatalf("account=%+v identities=%+v stored secret count=%d", store.account, store.identities, len(secrets.values))
	}
}

func TestConnectRollbackSurvivesRequestCancellation(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	ctx, cancel := context.WithCancel(t.Context())
	store.rejectCanceledContexts = true
	github.hostKeysHook = cancel
	github.hostKeysErr = context.Canceled

	_, err := manager.Connect(ctx, ConnectRequest{Target: target, AllowAuthorization: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if store.account.Provider != "" || len(store.identities) != 0 || len(secrets.values) != 0 {
		t.Fatalf("account=%+v identities=%+v stored secret count=%d", store.account, store.identities, len(secrets.values))
	}
}

func TestDisconnectRetriesUnboundAuthorizationCleanup(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	github.hostKeysErr = NewError(CodeSourceUnavailable, "GitHub metadata unavailable", nil)
	secrets.deleteErr = errors.New("keyring locked")

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if ErrorCode(err) != "outcome_unknown" || store.account.Provider == "" || len(secrets.values) != 1 || len(store.identities) != 0 {
		t.Fatalf("err=%v account=%+v stored secret count=%d identities=%+v", err, store.account, len(secrets.values), store.identities)
	}

	secrets.deleteErr = nil
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: target})
	if err != nil || !result.AccountRemoved || result.State != StatusNotConnected || store.account.Provider != "" || len(secrets.values) != 0 {
		t.Fatalf("result=%+v account=%+v stored secret count=%d err=%v", result, store.account, len(secrets.values), err)
	}
}

func TestDisconnectRevokesBeforeRecoverableBoxCleanup(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	target.removeErr = errors.New("Box unavailable")
	var phases []DisconnectPhase
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: target, RunPhase: func(phase DisconnectPhase, fn func() error) error {
		phases = append(phases, phase)
		return fn()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Revoked || !result.CleanupPending || result.Warning == "" || github.deleteCalls != 1 || len(phases) != 2 || phases[0] != DisconnectPhaseRevokingKey || phases[1] != DisconnectPhaseRemovingKey {
		t.Fatalf("result=%+v deleteCalls=%d phases=%v", result, github.deleteCalls, phases)
	}
	if store.identities[target.boxIdentity].State != StateCleanupPending {
		t.Fatalf("binding=%+v", store.identities[target.boxIdentity])
	}

	target.removeErr = nil
	status, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StatusNotConnected || store.account.Provider != "" || len(store.identities) != 0 {
		t.Fatalf("status=%+v account=%+v identities=%v", status, store.account, store.identities)
	}
}

func TestDisconnectPreviewDoesNotResumePendingCleanup(t *testing.T) {
	manager, store, _, _, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	binding := store.identities[target.boxIdentity]
	binding.State = StateCleanupPending
	store.identities[target.boxIdentity] = binding

	preview, err := manager.PreviewDisconnect(t.Context(), StatusRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if preview.BoxName != "work" || preview.Account.Login != "octocat" || preview.RemoteKeyTitle != "Schooner / work" || !preview.LastBox {
		t.Fatalf("preview=%+v", preview)
	}
	if target.removeCalls != 0 || store.identities[target.boxIdentity].State != StateCleanupPending || store.account.Provider == "" {
		t.Fatalf("removeCalls=%d binding=%+v account=%+v", target.removeCalls, store.identities[target.boxIdentity], store.account)
	}
}

func TestStatusReturnsPartialFactsWhenGitHubIsUnavailable(t *testing.T) {
	manager, _, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	github.listErr = NewError(CodeSourceUnavailable, "maintenance", nil)
	status, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StatusUnknown || status.Box.State != "present" || len(status.Warnings) == 0 {
		t.Fatalf("status=%+v", status)
	}
}

func TestStatusRejectsManagedTrustNotAdvertisedByGitHub(t *testing.T) {
	manager, _, _, _, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	staleFingerprint, err := PublicKeyFingerprint(testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	target.identity.HostFingerprints = []string{staleFingerprint}

	status, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil || status.State != StatusConflict || status.Box.State != "trust_changed" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestReauthorizationCannotSwitchAccountsWhileABoxIsBound(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	store.account.CredentialKey = ""
	manager.mu.Lock()
	delete(manager.ephemeral, GitHub)
	manager.mu.Unlock()
	github.account = RemoteAccount{ID: "99", Login: "other"}
	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
}

func TestMissingAccountRowCannotReplaceARetainedBindingWithAnotherAccount(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	store.account = Account{}
	secrets.values = map[string]string{}
	github.account = RemoteAccount{ID: "99", Login: "other"}
	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if ErrorCode(err) != "conflict" || store.account.Provider != "" || github.authorizeCalls != 2 {
		t.Fatalf("err=%v account=%+v authorizeCalls=%d", err, store.account, github.authorizeCalls)
	}
}

func TestConnectReconcilesAKeyCreatedBeforeALostResponse(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	github.createErr = NewError(CodeSourceUnavailable, "lost create response", nil)
	github.createEffect = true
	result, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteKeyID == 0 || github.createCalls != 1 || len(github.keys) != 1 || store.identities[target.boxIdentity].State != StateConnected {
		t.Fatalf("result=%+v calls=%d keys=%+v binding=%+v", result, github.createCalls, github.keys, store.identities[target.boxIdentity])
	}
}

func TestConnectIgnoresDuplicateTitlesAndMatchesOnlyFingerprint(t *testing.T) {
	manager, _, _, github, target := testManager(t)
	otherFingerprint, err := PublicKeyFingerprint(testHostKey)
	if err != nil {
		t.Fatal(err)
	}
	github.keys = []RemoteKey{{ID: 7, Title: "Schooner / work", PublicKey: testHostKey, Fingerprint: otherFingerprint}}
	result, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteKeyID == 7 || github.createCalls != 1 || len(github.keys) != 2 {
		t.Fatalf("result=%+v createCalls=%d keyCount=%d", result, github.createCalls, len(github.keys))
	}
}

func TestConnectingCheckpointRequiresConnectToRepeatSSHVerification(t *testing.T) {
	manager, store, _, _, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	binding := store.identities[target.boxIdentity]
	binding.State, binding.RemoteKeyID, binding.RemoteKeyTitle = StateConnecting, 0, ""
	store.identities[target.boxIdentity] = binding
	result, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	pending := store.identities[target.boxIdentity]
	if result.State != StatusActionRequired || pending.State != StateConnecting || pending.RemoteKeyID == 0 || len(result.Warnings) == 0 {
		t.Fatalf("result=%+v binding=%+v", result, pending)
	}
	recovered, err := manager.Connect(t.Context(), ConnectRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != StateConnected || store.identities[target.boxIdentity].State != StateConnected || target.verifyCalls != 2 {
		t.Fatalf("result=%+v binding=%+v verifyCalls=%d", recovered, store.identities[target.boxIdentity], target.verifyCalls)
	}
}

func TestFailedSSHVerificationKeepsARecoverableConnectingCheckpoint(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	target.verifyErr = NewError("authentication_required", "SSH key propagation is pending", nil)

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if ErrorCode(err) != "authentication_required" {
		t.Fatalf("err=%v", err)
	}
	binding := store.identities[target.boxIdentity]
	if binding.State != StateConnecting || binding.RemoteKeyID == 0 || len(github.keys) != 1 {
		t.Fatalf("binding=%+v keys=%+v", binding, github.keys)
	}
	status, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil || status.State != StatusActionRequired || store.identities[target.boxIdentity].State != StateConnecting || target.verifyCalls != 1 {
		t.Fatalf("status=%+v binding=%+v verifyCalls=%d err=%v", status, store.identities[target.boxIdentity], target.verifyCalls, err)
	}
	target.verifyErr = nil
	result, err := manager.Connect(t.Context(), ConnectRequest{Target: target})
	if err != nil || result.State != StateConnected || github.createCalls != 1 || target.verifyCalls != 2 {
		t.Fatalf("result=%+v createCalls=%d verifyCalls=%d err=%v", result, github.createCalls, target.verifyCalls, err)
	}
}

func TestRepositoryVerificationFailurePreservesConnectedBinding(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	target.verifyErr = NewError("authentication_required", "repository access was denied", nil)

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, Repository: "git@github.com:owner/missing.git"})
	if ErrorCode(err) != "authentication_required" {
		t.Fatalf("err=%v", err)
	}
	if binding := store.identities[target.boxIdentity]; binding.State != StateConnected || github.createCalls != 1 || target.ensureCalls != 1 || target.verifyCalls != 2 {
		t.Fatalf("binding=%+v createCalls=%d ensureCalls=%d verifyCalls=%d", binding, github.createCalls, target.ensureCalls, target.verifyCalls)
	}
}

func TestReconnectMutationReturnsToVerificationPending(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	github.keys = nil
	target.verifyErr = NewError("authentication_required", "replacement key propagation is pending", nil)

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: target, Repository: "git@github.com:owner/private.git"})
	if ErrorCode(err) != "authentication_required" {
		t.Fatalf("err=%v", err)
	}
	if binding := store.identities[target.boxIdentity]; binding.State != StateConnecting || github.createCalls != 2 || target.ensureCalls != 2 || target.verifyCalls != 2 {
		t.Fatalf("binding=%+v createCalls=%d ensureCalls=%d verifyCalls=%d", binding, github.createCalls, target.ensureCalls, target.verifyCalls)
	}
}

func TestStatusRecoversRevocationLostBeforeCleanupCheckpoint(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	binding := store.identities[target.boxIdentity]
	binding.State = StateDisconnecting
	store.identities[target.boxIdentity] = binding
	github.keys = nil // GitHub committed deletion before the response was lost.
	result, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StatusNotConnected || target.identity.Exists || len(store.identities) != 0 || store.account.Provider != "" || len(secrets.values) != 0 {
		t.Fatalf("result=%+v target=%+v account=%+v identities=%+v stored secret count=%d", result, target.identity, store.account, store.identities, len(secrets.values))
	}
}

func TestStatusDoesNotRevokeAnInterruptedDisconnect(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	binding := store.identities[target.boxIdentity]
	binding.State = StateDisconnecting
	store.identities[target.boxIdentity] = binding
	result, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StatusActionRequired || github.deleteCalls != 0 || !target.identity.Exists {
		t.Fatalf("result=%+v deleteCalls=%d target=%+v", result, github.deleteCalls, target.identity)
	}
}

func TestDisconnectNeverDeletesAReusedRemoteKeyID(t *testing.T) {
	manager, _, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	otherFingerprint, err := PublicKeyFingerprint(testHostKey)
	if err != nil {
		t.Fatal(err)
	}
	github.keys[0].Fingerprint = otherFingerprint
	_, err = manager.Disconnect(t.Context(), DisconnectRequest{Target: target})
	if ErrorCode(err) != "conflict" || github.deleteCalls != 0 {
		t.Fatalf("err=%v deleteCalls=%d", err, github.deleteCalls)
	}
}

func TestDisconnectTreatsDeleteNotFoundAsAlreadyRevoked(t *testing.T) {
	manager, _, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	github.deleteMissingAfterList = true
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: target})
	if err != nil || !result.Revoked || !result.BoxFilesRemoved || github.deleteCalls != 1 {
		t.Fatalf("result=%+v deleteCalls=%d err=%v", result, github.deleteCalls, err)
	}
}

func TestDisconnectRetainsCleanupCheckpointWithoutBoxConfirmation(t *testing.T) {
	manager, store, _, _, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	target.removeUnconfirmed = true
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: target})
	if err != nil || !result.Revoked || !result.CleanupPending || result.BoxFilesRemoved || store.identities[target.boxIdentity].State != StateCleanupPending {
		t.Fatalf("result=%+v binding=%+v err=%v", result, store.identities[target.boxIdentity], err)
	}
}

func TestDisconnectRevokesAuthorityButNeverDeletesAMismatchedBoxKey(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	replacementFingerprint, err := PublicKeyFingerprint(testHostKey)
	if err != nil {
		t.Fatal(err)
	}
	target.identity = HostIdentity{Provider: GitHub, Exists: true, PublicKey: testHostKey, Fingerprint: replacementFingerprint, TrustConfigured: true}
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: target})
	if err != nil || !result.Revoked || !result.CleanupPending || result.BoxFilesRemoved || github.deleteCalls != 1 || !target.identity.Exists {
		t.Fatalf("result=%+v target=%+v deleteCalls=%d err=%v", result, target.identity, github.deleteCalls, err)
	}
	if store.identities[target.boxIdentity].State != StateCleanupPending {
		t.Fatalf("binding=%+v", store.identities[target.boxIdentity])
	}
}

func TestPerBoxLockRejectsConcurrentSourceMutation(t *testing.T) {
	manager, _, _, github, target := testManager(t)
	lock, err := manager.acquire(target.boxIdentity, GitHub)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, err = manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if ErrorCode(err) != "operation_in_progress" || github.authorizeCalls != 0 {
		t.Fatalf("err=%v authorizeCalls=%d", err, github.authorizeCalls)
	}
}

func TestConcurrentBoxesShareExactlyOneAuthorizedAccount(t *testing.T) {
	manager, store, _, github, first := testManager(t)
	secondFingerprint, err := PublicKeyFingerprint(testHostKey)
	if err != nil {
		t.Fatal(err)
	}
	second := &fakeTarget{
		boxName: "other", boxIdentity: "22222222-2222-4222-8222-222222222222",
		identity: HostIdentity{Provider: GitHub, Exists: true, PublicKey: testHostKey, Fingerprint: secondFingerprint, TrustConfigured: true},
	}
	github.authorizeStarted = make(chan struct{})
	github.authorizeRelease = make(chan struct{})
	type concurrentResult struct {
		target *fakeTarget
		err    error
	}
	results := make(chan concurrentResult, 2)
	go func() {
		_, connectErr := manager.Connect(t.Context(), ConnectRequest{Target: first, AllowAuthorization: true})
		results <- concurrentResult{target: first, err: connectErr}
	}()
	<-github.authorizeStarted
	go func() {
		_, connectErr := manager.Connect(t.Context(), ConnectRequest{Target: second, AllowAuthorization: true})
		results <- concurrentResult{target: second, err: connectErr}
	}()
	concurrent := <-results
	if ErrorCode(concurrent.err) != "operation_in_progress" {
		t.Fatalf("concurrent error=%v", concurrent.err)
	}
	close(github.authorizeRelease)
	firstResult := <-results
	if firstResult.err != nil {
		t.Fatal(firstResult.err)
	}
	if _, err = manager.Connect(t.Context(), ConnectRequest{Target: concurrent.target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	if github.authorizeCalls != 1 || len(store.identities) != 2 || len(github.keys) != 2 {
		t.Fatalf("authorizeCalls=%d identities=%d keys=%d", github.authorizeCalls, len(store.identities), len(github.keys))
	}
	for _, binding := range store.identities {
		if binding.AccountExternalID != "42" || binding.State != StateConnected {
			t.Fatalf("binding=%+v", binding)
		}
	}
}

func TestExpiredCredentialRefreshRotatesStoredEnvelope(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	github.token.AccessExpiresAt = time.Now().Add(-time.Minute)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	oldKey := store.account.CredentialKey
	github.refreshToken = Token{AccessToken: "access-two", RefreshToken: "refresh-two", AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(24 * time.Hour)}
	if _, err := manager.Status(t.Context(), StatusRequest{Target: target}); err != nil {
		t.Fatal(err)
	}
	if github.refreshCalls != 1 || store.account.CredentialKey != oldKey || len(secrets.values) != 1 {
		t.Fatalf("refreshCalls=%d old=%q new=%q stored secret count=%d", github.refreshCalls, oldKey, store.account.CredentialKey, len(secrets.values))
	}
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
		Generation    string `json:"generation"`
		Token
	}
	if err := json.Unmarshal([]byte(secrets.values[store.account.CredentialKey]), &envelope); err != nil || envelope.SchemaVersion != "1" || envelope.Generation != store.account.CredentialGeneration || envelope.AccessToken != "access-two" || envelope.RefreshToken != "refresh-two" {
		t.Fatalf("envelope=%+v err=%v", envelope, err)
	}
}

func TestStatusWarnsWhenRefreshFallsBackToInvocationMemory(t *testing.T) {
	manager, _, secrets, github, target := testManager(t)
	github.token.AccessExpiresAt = time.Now().Add(-time.Minute)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	secrets.setErr = errors.New("keyring locked")
	github.refreshToken = Token{AccessToken: "access-two", RefreshToken: "refresh-two", AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(24 * time.Hour)}
	result, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil || result.State != StatusConnected || len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "credential storage") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestMalformedStoredCredentialNeverLeaksSecretMaterial(t *testing.T) {
	manager, store, secrets, _, _ := testManager(t)
	store.account = Account{Provider: GitHub, ExternalID: "42", Login: "octocat", CredentialKey: "opaque", CredentialGeneration: "generation", Status: "connected"}
	secrets.values["opaque"] = `{"schema_version":"1","generation":"generation","access_token":"must-not-leak"}`
	_, _, _, err := manager.resolveAccount(t.Context(), false, nil)
	if ErrorCode(err) != "authentication_required" || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialReferenceRecoversInterruptedFinalCheckpoint(t *testing.T) {
	manager, store, secrets, github, target := testManager(t)
	store.saveAccountErrAt = 2
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err == nil {
		t.Fatal("interrupted account checkpoint unexpectedly succeeded")
	}
	reference := store.account.CredentialKey
	if reference == "" || store.account.Status != "action_required" || len(secrets.values) != 1 || len(store.identities) != 0 {
		t.Fatalf("account=%+v stored secret count=%d identities=%d", store.account, len(secrets.values), len(store.identities))
	}
	store.saveAccountErrAt = 0
	result, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateConnected || store.account.CredentialKey != reference || store.account.Status != "connected" || len(secrets.values) != 1 || github.authorizeCalls != 1 {
		t.Fatalf("result=%+v account=%+v stored secret count=%d authorizeCalls=%d", result, store.account, len(secrets.values), github.authorizeCalls)
	}
}

func TestInterruptedCredentialRotationNeverTrustsThePreviousEnvelope(t *testing.T) {
	manager, store, secrets, _, _ := testManager(t)
	store.account = Account{
		Provider: GitHub, ExternalID: "42", Login: "octocat", CredentialKey: "opaque",
		CredentialGeneration: "new-generation", Status: "action_required",
	}
	secrets.values["opaque"] = `{"schema_version":"1","generation":"old-generation","access_token":"must-not-be-used","refresh_token":"refresh","access_expires_at":"2026-08-27T14:00:00Z","refresh_expires_at":"2026-08-28T14:00:00Z"}`
	_, _, _, err := manager.resolveAccount(t.Context(), false, nil)
	if ErrorCode(err) != "authentication_required" || strings.Contains(err.Error(), "must-not-be-used") || store.account.Status != "action_required" {
		t.Fatalf("err=%v account=%+v", err, store.account)
	}
}

func TestFinalCredentialCleanupRetriesAfterSecureStoreFailure(t *testing.T) {
	manager, store, secrets, _, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	secrets.deleteErr = errors.New("keyring locked")
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: target})
	if err != nil || !result.Revoked || !result.BoxFilesRemoved || !result.CleanupPending || result.Warning == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if store.identities[target.boxIdentity].State != StateCleanupPending {
		t.Fatalf("binding=%+v", store.identities[target.boxIdentity])
	}
	secrets.deleteErr = nil
	status, err := manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil || status.State != StatusNotConnected || store.account.Provider != "" || len(store.identities) != 0 {
		t.Fatalf("status=%+v account=%+v identities=%+v err=%v", status, store.account, store.identities, err)
	}
}

func TestFormerBoxNameCanRevokeBeforeMachineIsReadopted(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{BoxName: target.boxName})
	if err != nil || !result.Revoked || !result.CleanupPending || github.deleteCalls != 1 || !target.identity.Exists {
		t.Fatalf("result=%+v deleteCalls=%d target=%+v err=%v", result, github.deleteCalls, target.identity, err)
	}
	if store.identities[target.boxIdentity].State != StateCleanupPending || store.account.Provider == "" {
		t.Fatalf("binding=%+v account=%+v", store.identities[target.boxIdentity], store.account)
	}
	status, err := manager.Status(t.Context(), StatusRequest{BoxName: target.boxName})
	if err != nil || status.State != StatusCleanupPending {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	status, err = manager.Status(t.Context(), StatusRequest{Target: target})
	if err != nil || status.State != StatusNotConnected || target.identity.Exists {
		t.Fatalf("readopted status=%+v target=%+v err=%v", status, target.identity, err)
	}
}

func TestReusedBoxNameFallsBackToRetainedBindingWithoutTouchingReplacement(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	replacement := &fakeTarget{
		boxName:     target.boxName,
		boxIdentity: "22222222-2222-4222-8222-222222222222",
		identity:    target.identity,
	}

	status, err := manager.Status(t.Context(), StatusRequest{Target: replacement, BoxName: replacement.boxName})
	if err != nil || status.State != StatusUnknown || status.BoxIdentity != target.boxIdentity || status.Box.State != "unknown" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	result, err := manager.Disconnect(t.Context(), DisconnectRequest{Target: replacement, BoxName: replacement.boxName})
	if err != nil || !result.Revoked || !result.CleanupPending || result.BoxFilesRemoved || github.deleteCalls != 1 {
		t.Fatalf("result=%+v deleteCalls=%d err=%v", result, github.deleteCalls, err)
	}
	if !replacement.identity.Exists || store.identities[target.boxIdentity].State != StateCleanupPending {
		t.Fatalf("replacement=%+v binding=%+v", replacement.identity, store.identities[target.boxIdentity])
	}
}

func TestConnectRejectsReusedBoxNameBeforeCreatingAReplacementIdentity(t *testing.T) {
	manager, store, _, github, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	replacement := &fakeTarget{
		boxName:     target.boxName,
		boxIdentity: "22222222-2222-4222-8222-222222222222",
		identity:    target.identity,
	}

	_, err := manager.Connect(t.Context(), ConnectRequest{Target: replacement})
	if ErrorCode(err) != "conflict" || replacement.ensureCalls != 0 || github.createCalls != 1 || len(store.identities) != 1 {
		t.Fatalf("err=%v ensureCalls=%d createCalls=%d identities=%+v", err, replacement.ensureCalls, github.createCalls, store.identities)
	}
}

func TestDuplicateBindingsForReusedBoxNameReturnConflict(t *testing.T) {
	manager, store, _, _, target := testManager(t)
	if _, err := manager.Connect(t.Context(), ConnectRequest{Target: target, AllowAuthorization: true}); err != nil {
		t.Fatal(err)
	}
	replacement := &fakeTarget{
		boxName:     target.boxName,
		boxIdentity: "22222222-2222-4222-8222-222222222222",
		identity:    target.identity,
	}
	duplicate := store.identities[target.boxIdentity]
	duplicate.BoxIdentity = replacement.boxIdentity
	store.identities[replacement.boxIdentity] = duplicate

	_, err := manager.Status(t.Context(), StatusRequest{Target: replacement, BoxName: replacement.boxName})
	if ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
}

func testManager(t *testing.T) (*Manager, *memorySourceStore, *memorySecrets, *fakeGitHub, *fakeTarget) {
	t.Helper()
	publicKey := testPublicKey + " schooner:test"
	fingerprint, err := PublicKeyFingerprint(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := testHostKey
	hostFingerprint, err := PublicKeyFingerprint(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySourceStore{identities: map[string]BoxIdentity{}}
	secrets := &memorySecrets{values: map[string]string{}}
	github := &fakeGitHub{
		token:   Token{AccessToken: "token-one", RefreshToken: "refresh-one", AccessExpiresAt: time.Now().Add(time.Hour), RefreshExpiresAt: time.Now().Add(24 * time.Hour)},
		account: RemoteAccount{ID: "42", Login: "octocat"}, hostKeys: []HostKey{{Key: hostKey, Fingerprint: hostFingerprint}},
	}
	target := &fakeTarget{boxName: "work", boxIdentity: "11111111-1111-4111-8111-111111111111", identity: HostIdentity{Provider: GitHub, Exists: true, PublicKey: publicKey, Fingerprint: fingerprint, TrustConfigured: true}}
	manager, err := NewManager(store, secrets, github, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, secrets, github, target
}

type memorySourceStore struct {
	account                Account
	identities             map[string]BoxIdentity
	saveAccountCalls       int
	saveAccountErrAt       int
	rejectCanceledContexts bool
}

func (s *memorySourceStore) FindSourceAccount(context.Context, string) (Account, error) {
	if s.account.Provider == "" {
		return Account{}, NewError("not_found", "missing", nil)
	}
	return s.account, nil
}
func (s *memorySourceStore) SaveSourceAccount(_ context.Context, value Account) error {
	s.saveAccountCalls++
	if s.saveAccountErrAt == s.saveAccountCalls {
		return errors.New("account checkpoint unavailable")
	}
	s.account = value
	return nil
}
func (s *memorySourceStore) DeleteSourceAccount(context.Context, string) error {
	s.account = Account{}
	return nil
}
func (s *memorySourceStore) FindBoxSourceIdentity(_ context.Context, identity, _ string) (BoxIdentity, error) {
	value, ok := s.identities[identity]
	if !ok {
		return BoxIdentity{}, NewError("not_found", "missing", nil)
	}
	return value, nil
}
func (s *memorySourceStore) FindBoxSourceIdentityByName(_ context.Context, name, _ string) (BoxIdentity, error) {
	var result BoxIdentity
	matches := 0
	for _, value := range s.identities {
		if value.BoxName == name {
			result = value
			matches++
		}
	}
	if matches > 1 {
		return BoxIdentity{}, NewError("conflict", "multiple retained Box source identities use the former name", nil)
	}
	if matches == 1 {
		return result, nil
	}
	return BoxIdentity{}, NewError("not_found", "missing", nil)
}
func (s *memorySourceStore) ListBoxSourceIdentities(ctx context.Context, _ string) ([]BoxIdentity, error) {
	if s.rejectCanceledContexts && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result := make([]BoxIdentity, 0, len(s.identities))
	for _, value := range s.identities {
		result = append(result, value)
	}
	return result, nil
}
func (s *memorySourceStore) SaveBoxSourceIdentity(_ context.Context, value BoxIdentity) error {
	s.identities[value.BoxIdentity] = value
	return nil
}
func (s *memorySourceStore) DeleteBoxSourceIdentity(_ context.Context, identity, _ string) error {
	delete(s.identities, identity)
	return nil
}

type memorySecrets struct {
	values    map[string]string
	getErr    error
	setErr    error
	deleteErr error
}

func (s *memorySecrets) Get(key string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.values[key], nil
}
func (s *memorySecrets) Set(key, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.values[key] = value
	return nil
}
func (s *memorySecrets) Delete(key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, key)
	return nil
}

type fakeGitHub struct {
	token                  Token
	account                RemoteAccount
	hostKeys               []HostKey
	hostKeysErr            error
	hostKeysHook           func()
	keys                   []RemoteKey
	listErr                error
	accountErrOnce         error
	refreshToken           Token
	refreshErr             error
	createErr              error
	createEffect           bool
	deleteErr              error
	deleteMissingAfterList bool
	authorizeStarted       chan struct{}
	authorizeRelease       chan struct{}
	authorizeCalls         int
	refreshCalls           int
	listCalls              int
	accountCalls           int
	createCalls            int
	deleteCalls            int
	verifyCreateTitle      string
}

func (f *fakeGitHub) Authorize(context.Context) (Token, error) {
	f.authorizeCalls++
	if f.authorizeStarted != nil {
		close(f.authorizeStarted)
		<-f.authorizeRelease
		f.authorizeStarted = nil
	}
	return f.token, nil
}
func (f *fakeGitHub) Refresh(context.Context, string) (Token, error) {
	f.refreshCalls++
	if f.refreshErr != nil {
		return Token{}, f.refreshErr
	}
	if f.refreshToken.AccessToken != "" {
		return f.refreshToken, nil
	}
	return f.token, nil
}
func (f *fakeGitHub) Account(context.Context, string) (RemoteAccount, error) {
	f.accountCalls++
	if f.accountErrOnce != nil {
		err := f.accountErrOnce
		f.accountErrOnce = nil
		return RemoteAccount{}, err
	}
	return f.account, nil
}
func (f *fakeGitHub) HostKeys(context.Context) ([]HostKey, error) {
	if f.hostKeysHook != nil {
		f.hostKeysHook()
	}
	if f.hostKeysErr != nil {
		return nil, f.hostKeysErr
	}
	return f.hostKeys, nil
}
func (f *fakeGitHub) ListKeys(context.Context, string) ([]RemoteKey, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]RemoteKey(nil), f.keys...), nil
}
func (f *fakeGitHub) CreateKey(_ context.Context, _ string, title, publicKey string) (RemoteKey, error) {
	f.createCalls++
	f.verifyCreateTitle = title
	if f.createErr != nil && !f.createEffect {
		return RemoteKey{}, f.createErr
	}
	fingerprint, _ := PublicKeyFingerprint(publicKey)
	key := RemoteKey{ID: int64(100 + f.createCalls), Title: title, PublicKey: publicKey, Fingerprint: fingerprint}
	f.keys = append(f.keys, key)
	if f.createErr != nil {
		return RemoteKey{}, f.createErr
	}
	return key, nil
}
func (f *fakeGitHub) DeleteKey(_ context.Context, _ string, id int64) (bool, error) {
	f.deleteCalls++
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	if f.deleteMissingAfterList {
		f.keys = nil
		return false, nil
	}
	for index, key := range f.keys {
		if key.ID == id {
			f.keys = append(f.keys[:index], f.keys[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

type fakeTarget struct {
	boxName           string
	boxIdentity       string
	identity          HostIdentity
	ensureCalls       int
	removeCalls       int
	removeErr         error
	removeUnconfirmed bool
	verifyErr         error
	verifyCalls       int
}

func (f *fakeTarget) BoxName() string     { return f.boxName }
func (f *fakeTarget) BoxIdentity() string { return f.boxIdentity }
func (f *fakeTarget) InspectSourceIdentity(context.Context, string) (HostIdentity, error) {
	return f.identity, nil
}
func (f *fakeTarget) EnsureSourceIdentity(_ context.Context, request EnsureIdentityRequest) (HostIdentity, error) {
	f.ensureCalls++
	f.identity.HostFingerprints, _ = HostKeyFingerprints(request.HostKeys)
	return f.identity, nil
}
func (f *fakeTarget) RemoveSourceIdentity(_ context.Context, request RemoveIdentityRequest) (RemoveIdentityResult, error) {
	f.removeCalls++
	if f.removeErr != nil {
		return RemoveIdentityResult{}, f.removeErr
	}
	if f.identity.Exists && f.identity.Fingerprint != request.ExpectedFingerprint {
		return RemoveIdentityResult{}, NewError("conflict", "mismatched Box key", nil)
	}
	if f.removeUnconfirmed {
		return RemoveIdentityResult{Provider: GitHub}, nil
	}
	f.identity = HostIdentity{Provider: GitHub}
	return RemoveIdentityResult{Provider: GitHub, Removed: true}, nil
}
func (f *fakeTarget) VerifySourceRepository(context.Context, VerifyRequest) (VerifyResult, error) {
	f.verifyCalls++
	if f.verifyErr != nil {
		return VerifyResult{}, f.verifyErr
	}
	return VerifyResult{Provider: GitHub, Authenticated: true}, nil
}
