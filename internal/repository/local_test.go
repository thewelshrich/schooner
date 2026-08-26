package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOriginKeyMatchesCommonNetworkForms(t *testing.T) {
	want := "github.com/owner/repo"
	for _, value := range []string{
		"https://github.com/owner/repo.git",
		"https://user:token@GITHUB.com/owner/repo.git?credential=secret",
		"ssh://git@github.com:22/owner/repo.git",
		"git@github.com:owner/repo.git",
		"git://github.com:9418/owner/repo",
	} {
		if got := OriginKey(value); got != want {
			t.Errorf("OriginKey(%q) = %q, want %q", value, got, want)
		}
	}
	for _, value := range []string{"/tmp/repo", "../repo", "file:///tmp/repo", "s3://bucket/repo"} {
		if got := OriginKey(value); got != "" {
			t.Errorf("OriginKey(%q) = %q, want empty", value, got)
		}
	}
}

func TestOriginKeyRetainsNonDefaultSSHUsername(t *testing.T) {
	alice := "alice@example.com/owner/repo"
	for _, value := range []string{
		"alice@example.com:owner/repo.git",
		"alice@example.com:owner/repo.git/",
		"ssh://alice@example.com/owner/repo.git",
		"ssh://alice:secret@example.com:22/owner/repo.git/",
	} {
		if got := OriginKey(value); got != alice {
			t.Errorf("OriginKey(%q) = %q, want %q", value, got, alice)
		}
	}
	if got := OriginKey("bob@example.com:owner/repo.git"); got == alice {
		t.Errorf("OriginKey for a different SSH user = %q, want an independent identity", got)
	}
}

func TestOriginKeyNormalizesGitSuffixBeforeTrailingSlash(t *testing.T) {
	want := "example.com/owner/repo"
	for _, value := range []string{
		"https://example.com/owner/repo.git/",
		"git@example.com:owner/repo.git/",
	} {
		if got := OriginKey(value); got != want {
			t.Errorf("OriginKey(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestInspectLocalObservesContainingCheckoutWithoutPersistingState(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	mustGit(t, "init", repositoryPath)
	mustGitAt(t, repositoryPath, "config", "user.email", "test@example.com")
	mustGitAt(t, repositoryPath, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(repositoryPath, "tracked"), "initial\n")
	mustWrite(t, filepath.Join(repositoryPath, ".gitignore"), "ignored/\n")
	mustGitAt(t, repositoryPath, "add", "tracked", ".gitignore")
	mustGitAt(t, repositoryPath, "commit", "-m", "initial")
	mustGitAt(t, repositoryPath, "remote", "add", "origin", "https://user:token@example.com/owner/repo.git?credential=secret")
	nested := filepath.Join(repositoryPath, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repositoryPath, "untracked"), "local\n")
	if err := os.Mkdir(filepath.Join(repositoryPath, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repositoryPath, "ignored", "artifact"), "generated\n")

	checkout, err := InspectLocal(t.Context(), nested)
	if err != nil {
		t.Fatal(err)
	}
	wantTopLevel, err := filepath.EvalSymlinks(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if checkout == nil || checkout.TopLevel != wantTopLevel {
		t.Fatalf("checkout = %+v", checkout)
	}
	if checkout.Origin != "https://example.com/owner/repo" || checkout.OriginKey != "example.com/owner/repo" || checkout.CloneSource != "https://example.com/owner/repo.git" {
		t.Fatalf("origin = %q, key = %q, clone source = %q", checkout.Origin, checkout.OriginKey, checkout.CloneSource)
	}
	if checkout.Branch == "" || checkout.Detached || checkout.Upstream != "" || checkout.Status.Untracked != 1 || checkout.Status.Ignored != 0 {
		t.Fatalf("checkout = %+v", checkout)
	}

	mustGitAt(t, repositoryPath, "remote", "set-url", "origin", "ssh://alice:secret@example.com/owner/repo.git")
	checkout, err = InspectLocal(t.Context(), nested)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Origin != "ssh://example.com/owner/repo" || checkout.OriginKey != "alice@example.com/owner/repo" || checkout.CloneSource != "ssh://alice@example.com/owner/repo.git" {
		t.Fatalf("SSH origin = %q, key = %q, clone source = %q", checkout.Origin, checkout.OriginKey, checkout.CloneSource)
	}
}

func TestSanitizeCloneSourcePreservesOnlyRequiredSSHUsername(t *testing.T) {
	cases := map[string]string{
		"ssh://git:secret@example.com/owner/repo.git?token=secret#fragment": "ssh://git@example.com/owner/repo.git",
		"git@example.com:owner/repo.git?token=secret":                       "git@example.com:owner/repo.git",
		"https://user:secret@example.com/owner/repo.git?token=secret":       "https://example.com/owner/repo.git",
		"file:///tmp/repo": "",
	}
	for input, want := range cases {
		if got := sanitizeCloneSource(input); got != want {
			t.Errorf("sanitizeCloneSource(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInspectLocalReturnsNilOutsideGit(t *testing.T) {
	checkout, err := InspectLocal(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if checkout != nil {
		t.Fatalf("checkout = %+v, want nil", checkout)
	}
}

func TestInspectLocalSurfacesMalformedGitMetadata(t *testing.T) {
	directory := t.TempDir()
	mustWrite(t, filepath.Join(directory, ".git"), "not valid worktree metadata\n")
	checkout, err := InspectLocal(t.Context(), directory)
	if err == nil || checkout != nil {
		t.Fatalf("checkout = %+v, error = %v", checkout, err)
	}
}
