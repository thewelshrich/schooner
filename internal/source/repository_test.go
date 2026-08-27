package source

import "testing"

func TestRepositoryIdentityNormalizesGitHubTransports(t *testing.T) {
	want := "github.com/openai/codex"
	for _, value := range []string{
		"https://github.com/OpenAI/Codex.git",
		"ssh://git@github.com/OpenAI/Codex.git",
		"git@github.com:OpenAI/Codex.git",
	} {
		identity, network, err := RepositoryIdentityFor(value)
		if err != nil || !network || identity.Key() != want {
			t.Errorf("RepositoryIdentityFor(%q) = %+v, %t, %v", value, identity, network, err)
		}
		if identity.CanonicalSSH() != "git@github.com:openai/codex.git" || identity.CanonicalHTTPS() != "https://github.com/openai/codex.git" {
			t.Errorf("canonical transports for %q = %q, %q", value, identity.CanonicalSSH(), identity.CanonicalHTTPS())
		}
	}
}

func TestRepositoryIdentityRejectsCredentialBearingAndMalformedSources(t *testing.T) {
	for _, value := range []string{
		"https://token@github.com/openai/codex.git",
		"https://github.com/openai/codex.git?token=secret",
		"ssh://alice@github.com/openai/codex.git",
		"http://github.com/openai/codex.git",
		"ssh://git@github.com:2222/openai/codex.git",
		"git@github.com:openai/codex/extra.git",
		"alice@ github.com:openai/codex.git",
		`git@github.com:openai\codex.git`,
		`https://github.com/openai\codex.git`,
		"https://github.com/openai%2Fcodex.git",
		"https://github.com/openai%5Ccodex.git",
		"https://github.com/openai/../codex.git",
	} {
		if _, network, err := RepositoryIdentityFor(value); !network || err == nil {
			t.Errorf("RepositoryIdentityFor(%q) network=%t err=%v", value, network, err)
		}
	}
	if _, network, err := RepositoryIdentityFor("../local/repository"); err != nil || network {
		t.Fatalf("local path classified as network: network=%t err=%v", network, err)
	}
}

func TestRepositoryIdentityPreservesGenericNestedNamespace(t *testing.T) {
	identity, network, err := RepositoryIdentityFor("ssh://alice@gitlab.example.com/team/platform/tool.git")
	if err != nil || !network || identity.Key() != "ssh://alice@gitlab.example.com/absolute/team/platform/tool" || identity.CanonicalSSH() != "" {
		t.Fatalf("RepositoryIdentityFor() = %+v, %t, %v", identity, network, err)
	}
}

func TestRepositoryIdentityDistinguishesGenericSchemes(t *testing.T) {
	keys := map[string]bool{}
	for _, value := range []string{
		"https://git.example/team/repo.git",
		"git://git.example/team/repo.git",
		"ssh://git.example/team/repo.git",
	} {
		identity, network, err := RepositoryIdentityFor(value)
		if err != nil || !network || keys[identity.Key()] {
			t.Fatalf("RepositoryIdentityFor(%q)=%+v network=%t err=%v", value, identity, network, err)
		}
		keys[identity.Key()] = true
	}
}

func TestRepositoryIdentityAllowsOwnerlessGenericPaths(t *testing.T) {
	for _, value := range []string{"https://git.example/repo.git", "git@git.example:repo.git"} {
		identity, network, err := RepositoryIdentityFor(value)
		if err != nil || !network || identity.Owner != "" || identity.Repository != "repo" {
			t.Fatalf("RepositoryIdentityFor(%q)=%+v network=%t err=%v", value, identity, network, err)
		}
	}
}

func TestRepositoryIdentityDistinguishesGenericSSHPathRoots(t *testing.T) {
	relative, relativeNetwork, relativeErr := RepositoryIdentityFor("alice@git.example:team/repo.git")
	absolute, absoluteNetwork, absoluteErr := RepositoryIdentityFor("ssh://alice@git.example/team/repo.git")
	if relativeErr != nil || absoluteErr != nil || !relativeNetwork || !absoluteNetwork || relative.Key() == absolute.Key() || relative.Absolute || !absolute.Absolute {
		t.Fatalf("relative=%+v network=%t err=%v, absolute=%+v network=%t err=%v", relative, relativeNetwork, relativeErr, absolute, absoluteNetwork, absoluteErr)
	}
}

func TestRepositoryIdentityDistinguishesGenericSSHAccounts(t *testing.T) {
	alice, aliceNetwork, aliceErr := RepositoryIdentityFor("alice@git.example:team/repo.git")
	bob, bobNetwork, bobErr := RepositoryIdentityFor("bob@git.example:team/repo.git")
	if aliceErr != nil || bobErr != nil || !aliceNetwork || !bobNetwork || alice.Key() == bob.Key() || alice.Account != "alice" || bob.Account != "bob" {
		t.Fatalf("alice=%+v network=%t err=%v, bob=%+v network=%t err=%v", alice, aliceNetwork, aliceErr, bob, bobNetwork, bobErr)
	}
}
