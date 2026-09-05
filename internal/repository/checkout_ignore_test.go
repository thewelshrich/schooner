package repository

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckoutTransferRemovesIgnoreRule(t *testing.T) {
	for _, change := range []string{"committed", "unstaged", "deleted", "nested"} {
		t.Run(change, func(t *testing.T) {
			source := checkoutTestRepository(t)
			ignorePath, incomingPath := ".gitignore", "ignored.env"
			if change == "nested" {
				if err := os.Mkdir(filepath.Join(source, "nested"), 0o755); err != nil {
					t.Fatal(err)
				}
				ignorePath, incomingPath = "nested/.gitignore", "nested/ignored.env"
				writeCheckoutFile(t, filepath.Join(source, ignorePath), "ignored.env\n", 0o644)
				runCheckoutGit(t, "-C", source, "add", ".")
				runCheckoutGit(t, "-C", source, "commit", "-m", "nested ignore")
			}
			initial, err := CaptureCheckout(t.Context(), source, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer initial.Release()
			destination := filepath.Join(t.TempDir(), "destination")
			applyCheckoutCapture(t, destination, initial)
			destination, err = filepath.EvalSymlinks(destination)
			if err != nil {
				t.Fatal(err)
			}
			writeCheckoutFile(t, filepath.Join(source, ".gitignore"), "", 0o644)
			writeCheckoutFile(t, filepath.Join(source, ignorePath), "", 0o644)
			if change == "deleted" {
				runCheckoutGit(t, "-C", source, "rm", "-f", ignorePath)
			}
			if change != "unstaged" {
				runCheckoutGit(t, "-C", source, "add", "-u")
				runCheckoutGit(t, "-C", source, "commit", "-m", "remove ignore rule")
			}
			writeCheckoutFile(t, filepath.Join(source, incomingPath), "incoming\n", 0o644)
			incoming, err := CaptureCheckout(t.Context(), source, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer incoming.Release()
			expected, err := ObserveCheckout(t.Context(), destination)
			if err != nil {
				t.Fatal(err)
			}
			// A manifest page need not contain the incoming .gitignore.
			for _, entry := range incoming.State.Files {
				if entry.Path == incomingPath {
					if _, err := PreflightCheckoutFiles(t.Context(), destination, []CheckoutFile{entry}); err != nil {
						t.Fatal(err)
					}
				}
			}
			extracted, err := ExtractCheckoutPayload(incoming.PayloadPath, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer extracted.Release()
			if _, err := ApplyCheckoutIfUnchanged(t.Context(), destination, extracted, expected.RevalidationDigest); err != nil {
				t.Fatal(err)
			}
			if got := runCheckoutGit(t, "-C", destination, "status", "--porcelain=v2"); !bytes.Equal(got, runCheckoutGit(t, "-C", source, "status", "--porcelain=v2")) {
				t.Fatalf("status differs: %s", got)
			}
			if got, err := os.ReadFile(filepath.Join(destination, incomingPath)); err != nil || string(got) != "incoming\n" {
				t.Fatalf("incoming file = %q, %v", got, err)
			}
		})
	}
}

func TestPreflightRetainsDestinationLocalIgnoreRules(t *testing.T) {
	for _, rule := range []string{"info-exclude", "configured-exclude", "local-gitignore"} {
		t.Run(rule, func(t *testing.T) {
			destination := checkoutTestRepository(t)
			incomingPath := "ignored.env"
			switch rule {
			case "info-exclude":
				writeCheckoutFile(t, filepath.Join(destination, ".git", "info", "exclude"), "ignored.env\n", 0o600)
			case "configured-exclude":
				// Keep this relative to the destination to exercise Git's config semantics.
				writeCheckoutFile(t, filepath.Join(destination, ".git", "local-exclude"), "ignored.env\n", 0o600)
				runCheckoutGit(t, "-C", destination, "config", "core.excludesFile", ".git/local-exclude")
			case "local-gitignore":
				if err := os.Mkdir(filepath.Join(destination, "local"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeCheckoutFile(t, filepath.Join(destination, "local", ".gitignore"), "ignored.env\n", 0o600)
				incomingPath = "local/ignored.env"
			}
			before := runCheckoutGit(t, "-C", destination, "status", "--porcelain=v2")
			_, err := PreflightCheckoutFiles(t.Context(), destination, []CheckoutFile{{Path: incomingPath, Kind: "file"}})
			if ErrorCode(err) != CodeConflict {
				t.Fatalf("local ignore conflict = %v", err)
			}
			if after := runCheckoutGit(t, "-C", destination, "status", "--porcelain=v2"); !bytes.Equal(before, after) {
				t.Fatalf("preflight changed destination: %s", after)
			}
		})
	}
}
