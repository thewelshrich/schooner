package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/source"
)

const githubTestKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4f"

func TestDeviceAuthorizationPollsPendingAndSlowDown(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD-EFGH", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5})
		case "/login/oauth/access_token":
			polls++
			if polls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			} else if polls == 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "slow_down"})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "expires_in": 28800, "refresh_token": "refresh", "refresh_token_expires_in": 15811200})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	presenter := &recordingPresenter{}
	client := New(Options{ClientID: "client-id", HTTP: server.Client(), APIBase: server.URL, LoginBase: server.URL, Presenter: presenter})
	client.wait = func(context.Context, time.Duration) error { return nil }
	token, err := client.Authorize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "access" || token.RefreshToken != "refresh" || token.AccessExpiresAt.IsZero() || polls != 3 {
		t.Fatalf("token=%+v polls=%d", token, polls)
	}
	if presenter.authorization.UserCode != "ABCD-EFGH" || presenter.authorization.VerificationURI != "https://github.com/login/device" {
		t.Fatalf("authorization=%+v", presenter.authorization)
	}
}

func TestHostKeysAreValidatedAgainstMetadataFingerprints(t *testing.T) {
	key := githubTestKey
	fingerprint, _ := source.PublicKeyFingerprint(key)
	for _, advertised := range []string{strings.TrimPrefix(fingerprint, "SHA256:"), fingerprint} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ssh_keys": []string{key}, "ssh_key_fingerprints": map[string]string{"SHA256_ED25519": advertised}})
		}))
		client := New(Options{HTTP: server.Client(), APIBase: server.URL})
		keys, err := client.HostKeys(t.Context())
		server.Close()
		if err != nil || len(keys) != 1 || keys[0].Fingerprint != fingerprint {
			t.Fatalf("advertised=%q keys=%+v err=%v", advertised, keys, err)
		}
	}
}

func TestHostKeyFingerprintMismatchFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ssh_keys": []string{githubTestKey}, "ssh_key_fingerprints": map[string]string{"SHA256_ED25519": "SHA256:wrong"}})
	}))
	defer server.Close()
	client := New(Options{HTTP: server.Client(), APIBase: server.URL})
	_, err := client.HostKeys(t.Context())
	if source.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
}

func TestMalformedHostKeyFingerprintFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ssh_keys": []string{githubTestKey}, "ssh_key_fingerprints": map[string]string{"SHA256_ED25519": "not-a-fingerprint"}})
	}))
	defer server.Close()
	client := New(Options{HTTP: server.Client(), APIBase: server.URL})
	_, err := client.HostKeys(t.Context())
	if source.ErrorCode(err) != "conflict" {
		t.Fatalf("err=%v", err)
	}
}

func TestSSHKeyCRUDUsesBearerTokenAndSafeMetadata(t *testing.T) {
	key := githubTestKey
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-GitHub-Api-Version") != apiVersion {
			t.Errorf("api version=%q", r.Header.Get("X-GitHub-Api-Version"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/keys":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "title": "Schooner / work", "key": key}})
		case r.Method == http.MethodPost && r.URL.Path == "/user/keys":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 8, "title": "Schooner / other", "key": key})
		case r.Method == http.MethodDelete && r.URL.Path == "/user/keys/8":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := New(Options{HTTP: server.Client(), APIBase: server.URL})
	keys, err := client.ListKeys(t.Context(), "access")
	if err != nil || len(keys) != 1 || keys[0].ID != 7 || !strings.HasPrefix(keys[0].Fingerprint, "SHA256:") {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	created, err := client.CreateKey(t.Context(), "access", "Schooner / other", key)
	if err != nil || created.ID != 8 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	deleted, err := client.DeleteKey(t.Context(), "access", 8)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
}

func TestRefreshRotatesBothTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents := make(url.Values)
		_ = r.ParseForm()
		for key, values := range r.PostForm {
			contents[key] = values
		}
		if contents.Get("grant_type") != "refresh_token" || contents.Get("refresh_token") != "old" {
			t.Errorf("form=%v", contents)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "expires_in": 10, "refresh_token": "new-refresh", "refresh_token_expires_in": 20})
	}))
	defer server.Close()
	client := New(Options{ClientID: "client-id", HTTP: server.Client(), LoginBase: server.URL})
	token, err := client.Refresh(t.Context(), "old")
	if err != nil || token.AccessToken != "new-access" || token.RefreshToken != "new-refresh" {
		t.Fatalf("token=%+v err=%v", token, err)
	}
}

func TestDeviceAuthorizationReportsDenialAndExpiry(t *testing.T) {
	for _, outcome := range []string{"access_denied", "expired_token"} {
		t.Run(outcome, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login/device/code" {
					_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD-EFGH", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"error": outcome})
			}))
			defer server.Close()
			client := New(Options{ClientID: "client-id", HTTP: server.Client(), LoginBase: server.URL, Presenter: &recordingPresenter{}})
			client.wait = func(context.Context, time.Duration) error { return nil }
			_, err := client.Authorize(t.Context())
			if source.ErrorCode(err) != "authentication_required" || strings.Contains(err.Error(), "device-secret") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDeviceAuthorizationHonorsCancellationDuringPolling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD-EFGH", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
	}))
	defer server.Close()
	client := New(Options{ClientID: "client-id", HTTP: server.Client(), LoginBase: server.URL, Presenter: &recordingPresenter{}})
	client.wait = func(context.Context, time.Duration) error { return context.Canceled }
	_, err := client.Authorize(t.Context())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestListKeysFollowsBoundedPagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		page := r.URL.Query().Get("page")
		count := 1
		if page == "1" {
			count = 100
			w.Header().Set("Link", `<https://api.github.test/user/keys?page=2>; rel="next"`)
		}
		keys := make([]map[string]any, count)
		for index := range keys {
			id := index + 1
			if page == "2" {
				id = 101
			}
			keys[index] = map[string]any{"id": id, "title": fmt.Sprintf("key-%d", id), "key": githubTestKey}
		}
		_ = json.NewEncoder(w).Encode(keys)
	}))
	defer server.Close()
	client := New(Options{HTTP: server.Client(), APIBase: server.URL})
	keys, err := client.ListKeys(t.Context(), "access")
	if err != nil || len(keys) != 101 || requests != 2 {
		t.Fatalf("count=%d requests=%d err=%v", len(keys), requests, err)
	}
}

func TestMalformedAndOversizedGitHubResponsesAreRedacted(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "trailing JSON", body: `{"id":42,"login":"octocat"}{"access_token":"must-not-leak"}`},
		{name: "oversized", body: strings.Repeat("x", maxResponseBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, test.body) }))
			defer server.Close()
			client := New(Options{HTTP: server.Client(), APIBase: server.URL})
			_, err := client.Account(t.Context(), "access")
			if source.ErrorCode(err) != source.CodeSourceUnavailable || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTokenResponseRequiresExpiringRotatableCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-without-refresh"})
	}))
	defer server.Close()
	client := New(Options{ClientID: "client-id", HTTP: server.Client(), LoginBase: server.URL})
	_, err := client.Refresh(t.Context(), "old-refresh")
	if source.ErrorCode(err) != "authentication_required" || strings.Contains(err.Error(), "access-without-refresh") {
		t.Fatalf("err=%v", err)
	}
}

func TestDeviceAuthorizationRejectsUnsafeDisplayMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD\nINJECT", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
	}))
	defer server.Close()
	presenter := &recordingPresenter{}
	client := New(Options{ClientID: "client-id", HTTP: server.Client(), LoginBase: server.URL, Presenter: presenter})
	_, err := client.Authorize(t.Context())
	if source.ErrorCode(err) != source.CodeSourceUnavailable || presenter.authorization.UserCode != "" {
		t.Fatalf("authorization=%+v err=%v", presenter.authorization, err)
	}
}

func TestDeviceAuthorizationStopsAtLocalExpiryBeforeAnotherPoll(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/device/code" {
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD-EFGH", "verification_uri": "https://github.com/login/device", "expires_in": 1, "interval": 1})
			return
		}
		polls++
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
	}))
	defer server.Close()
	now := time.Now().UTC()
	client := New(Options{ClientID: "client-id", HTTP: server.Client(), LoginBase: server.URL, Presenter: &recordingPresenter{}})
	client.now = func() time.Time { return now }
	client.wait = func(context.Context, time.Duration) error { now = now.Add(time.Second); return nil }
	_, err := client.Authorize(t.Context())
	if source.ErrorCode(err) != "authentication_required" || polls != 0 {
		t.Fatalf("polls=%d err=%v", polls, err)
	}
}

type recordingPresenter struct{ authorization source.DeviceAuthorization }

func (p *recordingPresenter) Present(_ context.Context, value source.DeviceAuthorization) error {
	p.authorization = value
	return nil
}

func (p *recordingPresenter) Wait(ctx context.Context, _ string, action func(context.Context) error) error {
	if action == nil {
		return nil
	}
	return action(ctx)
}
