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
	if err != nil || !network || identity.Key() != "ssh://alice@gitlab.example.com/absolute/team/platform/tool.git" || identity.CanonicalSSH() != "" {
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
		if err != nil || !network || identity.Owner != "" || identity.Repository != "repo.git" {
			t.Fatalf("RepositoryIdentityFor(%q)=%+v network=%t err=%v", value, identity, network, err)
		}
	}
}

func TestRepositoryIdentityDistinguishesGenericGitSuffixes(t *testing.T) {
	plain, plainNetwork, plainErr := RepositoryIdentityFor("https://git.example/team/repo")
	suffixed, suffixedNetwork, suffixedErr := RepositoryIdentityFor("https://git.example/team/repo.git")
	if plainErr != nil || suffixedErr != nil || !plainNetwork || !suffixedNetwork || plain.Key() == suffixed.Key() {
		t.Fatalf("plain=%+v network=%t err=%v, suffixed=%+v network=%t err=%v", plain, plainNetwork, plainErr, suffixed, suffixedNetwork, suffixedErr)
	}
}

func TestRepositoryIdentityDistinguishesExplicitGenericDefaultPort(t *testing.T) {
	implicit, implicitNetwork, implicitErr := RepositoryIdentityFor("ssh://alice@git.example/team/repo.git")
	explicit, explicitNetwork, explicitErr := RepositoryIdentityFor("ssh://alice@git.example:22/team/repo.git")
	if implicitErr != nil || explicitErr != nil || !implicitNetwork || !explicitNetwork || implicit.Key() == explicit.Key() {
		t.Fatalf("implicit=%+v network=%t err=%v, explicit=%+v network=%t err=%v", implicit, implicitNetwork, implicitErr, explicit, explicitNetwork, explicitErr)
	}
}

func TestRepositoryIdentityDistinguishesGenericSSHPathRoots(t *testing.T) {
	relative, relativeNetwork, relativeErr := RepositoryIdentityFor("alice@git.example:team/repo.git")
	absolute, absoluteNetwork, absoluteErr := RepositoryIdentityFor("ssh://alice@git.example/team/repo.git")
	if relativeErr != nil || absoluteErr != nil || !relativeNetwork || !absoluteNetwork || relative.Key() == absolute.Key() || relative.Absolute || !absolute.Absolute {
		t.Fatalf("relative=%+v network=%t err=%v, absolute=%+v network=%t err=%v", relative, relativeNetwork, relativeErr, absolute, absoluteNetwork, absoluteErr)
	}
}

func TestRepositoryIdentityMatchesSSHTildeAndSCPHomeRelativePaths(t *testing.T) {
	urlIdentity, urlNetwork, urlErr := RepositoryIdentityFor("ssh://alice@git.example/~/repo.git")
	scpIdentity, scpNetwork, scpErr := RepositoryIdentityFor("alice@git.example:~/repo.git")
	if urlErr != nil || scpErr != nil || !urlNetwork || !scpNetwork || urlIdentity.Key() != scpIdentity.Key() || urlIdentity.Absolute || scpIdentity.Absolute {
		t.Fatalf("url=%+v network=%t err=%v, scp=%+v network=%t err=%v", urlIdentity, urlNetwork, urlErr, scpIdentity, scpNetwork, scpErr)
	}
}

func TestRepositoryIdentityAllowsEscapedSpacesInGenericPaths(t *testing.T) {
	identity, network, err := RepositoryIdentityFor("https://git.example/team/my%20repo.git")
	if err != nil || !network || identity.Owner != "team" || identity.Repository != "my repo.git" {
		t.Fatalf("RepositoryIdentityFor()=%+v network=%t err=%v", identity, network, err)
	}
}

func TestRepositoryIdentityDistinguishesGenericSSHAccounts(t *testing.T) {
	alice, aliceNetwork, aliceErr := RepositoryIdentityFor("alice@git.example:team/repo.git")
	bob, bobNetwork, bobErr := RepositoryIdentityFor("bob@git.example:team/repo.git")
	if aliceErr != nil || bobErr != nil || !aliceNetwork || !bobNetwork || alice.Key() == bob.Key() || alice.Account != "alice" || bob.Account != "bob" {
		t.Fatalf("alice=%+v network=%t err=%v, bob=%+v network=%t err=%v", alice, aliceNetwork, aliceErr, bob, bobNetwork, bobErr)
	}
}
