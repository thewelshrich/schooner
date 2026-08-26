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

func TestInspectLocalObservesContainingCheckoutWithoutPersistingState(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repo")
	mustGit(t, "init", repositoryPath)
	mustGitAt(t, repositoryPath, "config", "user.email", "test@example.com")
	mustGitAt(t, repositoryPath, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(repositoryPath, "tracked"), "initial\n")
	mustGitAt(t, repositoryPath, "add", "tracked")
	mustGitAt(t, repositoryPath, "commit", "-m", "initial")
	mustGitAt(t, repositoryPath, "remote", "add", "origin", "https://user:token@example.com/owner/repo.git?credential=secret")
	nested := filepath.Join(repositoryPath, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(repositoryPath, "untracked"), "local\n")

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
	if checkout.Origin != "https://example.com/owner/repo" || checkout.OriginKey != "example.com/owner/repo" || checkout.CloneSource != "https://example.com/owner/repo" {
		t.Fatalf("origin = %q, key = %q, clone source = %q", checkout.Origin, checkout.OriginKey, checkout.CloneSource)
	}
	if checkout.Branch == "" || checkout.Detached || checkout.Upstream != "" || checkout.Status.Untracked != 1 {
		t.Fatalf("checkout = %+v", checkout)
	}
}

func TestSanitizeCloneSourcePreservesOnlyRequiredSSHUsername(t *testing.T) {
	cases := map[string]string{
		"ssh://git:secret@example.com/owner/repo.git?token=secret#fragment": "ssh://git@example.com/owner/repo",
		"git@example.com:owner/repo.git?token=secret":                       "git@example.com:owner/repo",
		"https://user:secret@example.com/owner/repo.git?token=secret":       "https://example.com/owner/repo",
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
