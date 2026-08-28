package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/source"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func TestSourceJSONDocumentsAreVersionedSafeAndIncludeWarnings(t *testing.T) {
	tests := []struct {
		name   string
		write  func(*bytes.Buffer) error
		checks map[string]any
		golden string
	}{
		{
			name: "connect",
			write: func(output *bytes.Buffer) error {
				return writeSourceConnect(output, "json", source.ConnectResult{
					Provider: source.GitHub, Account: source.RemoteAccount{ID: "42", Login: "octocat"}, BoxName: "work",
					BoxIdentity: "11111111-1111-4111-8111-111111111111", Fingerprint: "SHA256:safe", RemoteKeyID: 7,
					RemoteKeyTitle: "Schooner / work", State: source.StateConnected, Warning: "credential storage unavailable",
				}, nil)
			},
			checks: map[string]any{"provider": "github", "state": "connected"},
			golden: "source-connect.json",
		},
		{
			name: "status",
			write: func(output *bytes.Buffer) error {
				return writeSourceStatus(output, "json", source.StatusResult{
					Provider: source.GitHub, BoxName: "work", State: source.StatusUnknown,
					Local:  source.LayerObservation{State: "connected", Fingerprint: "SHA256:safe"},
					Box:    source.LayerObservation{State: "present", Fingerprint: "SHA256:safe"},
					Remote: source.LayerObservation{State: "unknown"}, Warnings: []string{"GitHub unavailable"},
				}, nil)
			},
			checks: map[string]any{"provider": "github", "state": "unknown"},
			golden: "source-status.json",
		},
		{
			name: "disconnect",
			write: func(output *bytes.Buffer) error {
				return writeSourceDisconnect(output, "json", source.DisconnectResult{
					Provider: source.GitHub, Account: source.RemoteAccount{ID: "42", Login: "octocat"}, BoxName: "work",
					State: source.StatusCleanupPending, Fingerprint: "SHA256:safe", RemoteKeyID: 7, RemoteKeyTitle: "Schooner / work",
					Local: source.LayerObservation{State: "cleanup_pending", Fingerprint: "SHA256:safe"},
					Box:   source.LayerObservation{State: "unknown"}, Remote: source.LayerObservation{State: "revoked"},
					Revoked: true, CleanupPending: true, Warning: "Box cleanup pending",
				}, nil)
			},
			checks: map[string]any{"provider": "github", "cleanup_pending": true},
			golden: "source-disconnect.json",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.write(&output); err != nil {
				t.Fatal(err)
			}
			if want := sourceGolden(t, test.golden); output.String() != want {
				t.Fatalf("output mismatch\n--- want ---\n%s--- got ---\n%s", want, output.String())
			}
			var document map[string]any
			if err := json.Unmarshal(output.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if document["schema_version"] != "1" {
				t.Fatalf("document=%v", document)
			}
			for key, value := range test.checks {
				if document[key] != value {
					t.Fatalf("%s=%v, want %v", key, document[key], value)
				}
			}
			warnings, ok := document["warnings"].([]any)
			if !ok || len(warnings) == 0 {
				t.Fatalf("warnings=%v", document["warnings"])
			}
			serialized := strings.ToLower(output.String())
			for _, excluded := range []string{"access_token", "refresh_token", "private_key", "/.local/state/", "device_code"} {
				if strings.Contains(serialized, excluded) {
					t.Fatalf("output contains excluded field %q", excluded)
				}
			}
		})
	}
}

func TestSourceConnectHumanOutputGolden(t *testing.T) {
	var output bytes.Buffer
	err := writeSourceConnect(&output, "human", source.ConnectResult{
		Provider: source.GitHub, Account: source.RemoteAccount{ID: "42", Login: "octocat"}, BoxName: "work",
		BoxIdentity: "11111111-1111-4111-8111-111111111111", Fingerprint: "SHA256:safe", RemoteKeyID: 7,
		RemoteKeyTitle: "Schooner / work", State: source.StateConnected,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := sourceGolden(t, "source-connect.txt"); output.String() != want {
		t.Fatalf("output mismatch\n--- want ---\n%s--- got ---\n%s", want, output.String())
	}
}

func TestWriteExplanationWrapsWithoutColor(t *testing.T) {
	var output bytes.Buffer
	if err := writeExplanation(&output, nil, "This explanation should wrap in plain output even when terminal color is disabled, so long-running command guidance remains easy to scan."); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped output, got %q", output.String())
	}
	for _, line := range lines {
		if len(line) > 72 {
			t.Fatalf("line width = %d, want <= 72: %q", len(line), line)
		}
	}
}

func TestDevicePresenterAlwaysPrintsInstructionsAndBrowserFailureIsNonfatal(t *testing.T) {
	var diagnostics bytes.Buffer
	called := ""
	presenter := &devicePresenter{
		streams: Streams{Err: &diagnostics}, goos: "darwin",
		run: func(_ context.Context, name string, arguments ...string) error {
			called = name + " " + strings.Join(arguments, " ")
			return errors.New("browser unavailable")
		},
	}
	authorization := source.DeviceAuthorization{VerificationURI: "https://github.com/login/device", UserCode: "ABCD-EFGH"}
	if err := presenter.Present(t.Context(), authorization); err != nil {
		t.Fatal(err)
	}
	want := "\nAuthorize Schooner\n  Open  https://github.com/login/device\n  Code  ABCD-EFGH\n\nIn the browser, authorize Git SSH key access. That lets Schooner\nregister this Box's public key. It does not grant repository access.\n\nWarning: Could not open a browser automatically; use the URL and code above.\n"
	if called != "open https://github.com/login/device" || diagnostics.String() != want {
		t.Fatalf("called=%q diagnostics=%q", called, diagnostics.String())
	}
}

func TestDevicePresenterUsesTerminalTheme(t *testing.T) {
	var diagnostics bytes.Buffer
	presenter := &devicePresenter{
		streams: Streams{Err: &diagnostics}, theme: uitheme.New(uitheme.Dark, true), goos: "other",
		run: func(context.Context, string, ...string) error { return nil },
	}
	if err := presenter.Present(t.Context(), source.DeviceAuthorization{VerificationURI: "https://github.com/login/device", UserCode: "ABCD-EFGH"}); err != nil {
		t.Fatal(err)
	}
	if output := diagnostics.String(); !strings.Contains(output, "\x1b[") || !strings.Contains(output, "Authorize Schooner") || !strings.Contains(output, "Git SSH key access") {
		t.Fatalf("themed output=%q", output)
	}
}

func TestDevicePresenterWaitShowsAccessibleStatus(t *testing.T) {
	var diagnostics bytes.Buffer
	presenter := &devicePresenter{streams: Streams{Err: &diagnostics}, accessible: true}
	called := false
	if err := presenter.Wait(t.Context(), "Waiting for GitHub authorization…", func(context.Context) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	if !strings.Contains(diagnostics.String(), "✓ Waiting for GitHub authorization") {
		t.Fatalf("output=%q", diagnostics.String())
	}
}

func TestDevicePresenterRejectsUntrustedAuthorizationURL(t *testing.T) {
	presenter := &devicePresenter{streams: Streams{Err: &bytes.Buffer{}}, goos: "linux", run: func(context.Context, string, ...string) error { return nil }}
	if err := presenter.Present(t.Context(), source.DeviceAuthorization{VerificationURI: "https://example.com/login/device", UserCode: "ABCD"}); err == nil {
		t.Fatal("untrusted device URL was accepted")
	}
}

func TestNonInteractiveDisconnectRequiresExplicitConfirmation(t *testing.T) {
	command := newSourceDisconnectCommand(Streams{}, &globalOptions{output: "human", noInput: true}, nil)
	command.SetArgs([]string{source.GitHub})
	err := command.ExecuteContext(t.Context())
	var usage usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err=%v", err)
	}
}

func TestSAMLGuidanceUsesSafeOrganizationContext(t *testing.T) {
	for name, err := range map[string]error{
		"source": &source.Error{Code: "authentication_required", Message: "SAML authorization required", Context: map[string]string{"reason": "github_saml_sso", "organization": "acme-tools"}},
		"box":    &box.Error{Code: "authentication_required", Message: "SAML authorization required", Context: map[string]string{"reason": "github_saml_sso", "organization": "acme-tools"}},
	} {
		t.Run(name, func(t *testing.T) {
			guided := withSourceGuidance(err, "work")
			var guidance guidanceError
			if !errors.As(guided, &guidance) || !strings.Contains(guidance.guidance, "acme-tools") || !strings.Contains(guidance.guidance, "Schooner / work") {
				t.Fatalf("guided=%v", guided)
			}
		})
	}
}

func sourceGolden(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
