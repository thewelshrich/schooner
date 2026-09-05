package boxgit

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thewelshrich/schooner/internal/process"
	"github.com/thewelshrich/schooner/internal/source"
)

func TestVerifyRepositoryAccessWithoutHEAD(t *testing.T) {
	for _, name := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is required", name)
		}
	}
	manager, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(t.Context(), testEnsureRequest(t)); err != nil {
		t.Fatal(err)
	}
	request := source.VerifyRequest{Provider: source.GitHub, Repository: "git@github.com:owner/empty.git"}

	t.Run("accessible empty repository", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "empty.git")
		if output, err := exec.CommandContext(t.Context(), "git", "init", "--bare", repository).CombinedOutput(); err != nil {
			t.Fatalf("init: %v: %s", err, output)
		}
		manager.run = localRepositoryRunner{remote: request.Repository, local: repository}
		result, err := manager.Verify(t.Context(), request)
		if err != nil || !result.Authenticated || result.Provider != source.GitHub {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("authentication denied", func(t *testing.T) {
		manager.run = interceptRunner{
			delegate: osRunner{},
			result:   process.Result{Stderr: []byte("git@github.com: Permission denied (publickey).")},
			err:      errors.New("exit status 128"),
		}
		result, err := manager.Verify(t.Context(), request)
		if result.Authenticated || source.ErrorCode(err) != "authentication_required" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

// Keep Git's actual ref-advertisement behavior while using only a local fixture.
type localRepositoryRunner struct {
	remote string
	local  string
}

func (r localRepositoryRunner) Run(ctx context.Context, environment []string, name string, arguments ...string) (process.Result, error) {
	if name == "git" {
		arguments = append([]string(nil), arguments...)
		for i, argument := range arguments {
			if argument == r.remote {
				arguments[i] = r.local
			}
		}
	}
	return (osRunner{}).Run(ctx, environment, name, arguments...)
}
