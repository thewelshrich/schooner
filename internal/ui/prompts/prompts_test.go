package prompts

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/provider"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func TestAddAccessibleReviewAndConfirm(t *testing.T) {
	var output bytes.Buffer
	draft, confirmed, err := Add(t.Context(), Options{Input: &oneByteReader{reader: strings.NewReader("1\ny\n")}, Output: &output, Accessible: true}, AddDraft{Name: "work", SSHDestination: "work-host", WorktreeRoot: "~/schooner"}, true, true, true, false)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !confirmed || draft.Name != "work" {
		t.Fatalf("draft = %+v, confirmed = %v", draft, confirmed)
	}
	got := output.String()
	if !strings.Contains(got, "Existing SSH") || !strings.Contains(got, "Review") {
		t.Fatalf("output = %q", got)
	}
	if !strings.Contains(got, "  SSH destination  work-host\n") || strings.Contains(got, "Destination:") {
		t.Fatalf("review labels should match settled chrome: %q", got)
	}
}

func TestYesNoAndPromptCount(t *testing.T) {
	if yesNo(true) != "Yes" || yesNo(false) != "No" {
		t.Fatalf("yesNo = %q / %q", yesNo(true), yesNo(false))
	}
	if promptCount(0) != "none" || promptCount(3) != "3" {
		t.Fatalf("promptCount = %q / %q", promptCount(0), promptCount(3))
	}
}

func TestWaitAccessiblePrintsStaticStatus(t *testing.T) {
	var output bytes.Buffer
	called := false
	err := Wait(t.Context(), Options{Output: &output, Accessible: true}, "Loading DigitalOcean catalog", func(context.Context) error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
	if output.String() != "… Loading DigitalOcean catalog\n✓ Loading DigitalOcean catalog\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestExplainWrapsAccessibleText(t *testing.T) {
	var output bytes.Buffer
	explain(Options{Output: &output, Accessible: true}, "This explanation should wrap in accessible output even when terminal color and styling are disabled, so it remains easy to scan.")

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped output, got %q", output.String())
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 72 {
			t.Fatalf("line width = %d, want <= 72: %q", width, line)
		}
	}
}

func TestCatalogOptionSplitsMutedDetail(t *testing.T) {
	plain := catalogOption(Options{Accessible: true}, "Amsterdam 3", "ams3")
	if plain != "Amsterdam 3  ams3" {
		t.Fatalf("accessible option = %q", plain)
	}
	colored := catalogOption(Options{Theme: uitheme.New(uitheme.Dark, true)}, "Amsterdam 3", "ams3")
	if !strings.Contains(colored, "Amsterdam 3  ") || !strings.Contains(colored, "ams3") || colored == plain {
		t.Fatalf("colored option should mute detail: %q", colored)
	}
	if !strings.Contains(colored, "\x1b[") {
		t.Fatalf("colored option missing ANSI: %q", colored)
	}
}

func TestSizeCatalogOptionHighlightsPrice(t *testing.T) {
	plain := sizeCatalogOption(Options{Accessible: true}, "s-1vcpu-512mb-10gb", 1, 512, 4)
	if plain != "s-1vcpu-512mb-10gb  1 vCPU · 512 MB · $4.00/mo" {
		t.Fatalf("accessible size option = %q", plain)
	}
	theme := uitheme.New(uitheme.Dark, true)
	colored := sizeCatalogOption(Options{Theme: theme}, "s-1vcpu-512mb-10gb", 1, 512, 4)
	price := theme.Style(uitheme.Primary).Render("$4.00/mo")
	specs := theme.Style(uitheme.Muted).Render("1 vCPU · 512 MB · ")
	if !strings.HasPrefix(colored, "s-1vcpu-512mb-10gb  ") || !strings.Contains(colored, specs) || !strings.Contains(colored, price) {
		t.Fatalf("size option = %q", colored)
	}
}

func TestDigitalOceanProvisionSeparatesLocalAndAccountSSHKeys(t *testing.T) {
	var output bytes.Buffer
	local := provider.PublicKey{Name: "id_ed25519", Fingerprint: "SHA256:local", PublicKey: "ssh-ed25519 AAAA"}
	draft := ProvisionDraft{Name: "work", WorktreeRoot: "~/schooner", Region: "fra1", Size: "small", Image: "ubuntu-24-04-x64", LocalPublicKeys: []provider.PublicKey{local}, AccessKeyIDs: []string{"9"}, IPv6: true}
	catalog := provider.Catalog{AccessKeys: []provider.AccessKey{{ID: "9", Name: "work-laptop", Fingerprint: "fp-do"}}}
	options := Options{Input: strings.NewReader("0\n0\nn\ny\n"), Output: &output, Accessible: true, Summary: NewChoiceSummary()}
	got, err := DigitalOceanProvision(t.Context(), options, draft, catalog, []provider.PublicKey{local}, true, true, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LocalPublicKeys) != 1 || len(got.AccessKeyIDs) != 1 {
		t.Fatalf("draft = %+v", got)
	}
	for _, title := range []string{"Local SSH keys", "DigitalOcean account SSH keys"} {
		if !strings.Contains(output.String(), title) {
			t.Fatalf("output %q does not contain %q", output.String(), title)
		}
	}
}

func TestAddAccessibleDecline(t *testing.T) {
	var output bytes.Buffer
	_, confirmed, err := Add(t.Context(), Options{Input: &oneByteReader{reader: strings.NewReader("1\nn\n")}, Output: &output, Accessible: true}, AddDraft{Name: "work", SSHDestination: "work-host", WorktreeRoot: "~/schooner"}, true, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("declined form was confirmed")
	}
}

func TestChooseAcquisitionRendersSettledChoiceOnce(t *testing.T) {
	var output bytes.Buffer
	summary := NewChoiceSummary()
	options := Options{Input: strings.NewReader("1\n"), Output: &output, Accessible: true, Summary: summary}
	method, err := ChooseAcquisition(t.Context(), options)
	if err != nil || method != "ssh" {
		t.Fatalf("method = %q, err = %v", method, err)
	}
	first := output.String()
	if strings.Contains(first, "Choices so far") || strings.Contains(first, "│ Acquisition") {
		t.Fatalf("cumulative table should be gone: %q", first)
	}
	if !strings.Contains(first, "\nAcquisition\n") || !strings.Contains(first, "  Acquisition  Existing SSH\n") {
		t.Fatalf("settled acquisition missing from %q", first)
	}
	beforeName := output.Len()
	RecordChoices(options, Choice{Label: "Name", Value: "work"})
	delta := output.String()[beforeName:]
	if delta != "  Name  work\n" {
		t.Fatalf("name delta = %q", delta)
	}
	if strings.Contains(output.String(), "┌") || strings.Count(output.String(), "  Acquisition  Existing SSH\n") != 1 {
		t.Fatalf("prior choices must not be reprinted: %q", output.String())
	}
}

func TestSectionAndKeyValuesUseTightStepRhythm(t *testing.T) {
	var output bytes.Buffer
	options := Options{Output: &output}
	section(options, "Box details")
	renderKeyValues(options,
		Choice{Label: "Name", Value: "work"},
		Choice{Label: "Worktree root", Value: "~/schooner"},
	)
	section(options, "Review")
	renderKeyValues(options, Choice{Label: "Name", Value: "work"})
	got := output.String()
	want := "\nBox details\n  Name           work\n  Worktree root  ~/schooner\n\nReview\n  Name  work\n"
	if got != want {
		t.Fatalf("rhythm = %q, want %q", got, want)
	}
}

func TestBeginReviewClearsPriorSteps(t *testing.T) {
	var output bytes.Buffer
	if err := beginReview(t.Context(), Options{Output: &output, Theme: uitheme.New(uitheme.Dark, true)}); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.HasPrefix(got, ansi.EraseEntireScreen+ansi.CursorHomePosition) {
		t.Fatalf("review should clear first: %q", got)
	}
	if !strings.Contains(got, "schooner") || !strings.Contains(got, "▁▂▄▆▆▄▂▁") || !strings.Contains(got, "Review") {
		t.Fatalf("review should keep heading then title: %q", got)
	}
	output.Reset()
	if err := beginReview(t.Context(), Options{Output: &output, Accessible: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b[") || output.String() != "\nReview\n" {
		t.Fatalf("accessible review must stay plain: %q", output.String())
	}
}

type oneByteReader struct{ reader *strings.Reader }

func (r *oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return r.reader.Read(buffer)
}

func TestPickBoxAccessible(t *testing.T) {
	var output bytes.Buffer
	name, err := PickBox(t.Context(), Options{Input: strings.NewReader("2\n"), Output: &output, Accessible: true}, "Choose", []box.Record{{Name: "one", SSHDestination: "one-host"}, {Name: "two", SSHDestination: "two-host"}})
	if err != nil || name != "two" {
		t.Fatalf("name = %q, err = %v", name, err)
	}
}

func TestConfirmRemoveStatesRemoteIsUnchanged(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmRemove(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true}, box.Record{Name: "work", SSHDestination: "work-host"})
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	if !strings.Contains(output.String(), "remote machine") || !strings.Contains(output.String(), "remain unchanged") {
		t.Fatalf("output = %q", output.String())
	}
	if !strings.Contains(output.String(), "Disconnect source access first") || !strings.Contains(output.String(), "never calls GitHub") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestConfirmDestroyWarnsAboutSeparateSourceCleanup(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmDestroy(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true}, box.Record{Name: "work", ProviderResourceID: "42"})
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	if !strings.Contains(output.String(), "Disconnect source access first") || !strings.Contains(output.String(), "never calls GitHub") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestConfirmGitHubConnectExplainsDeviceFlow(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmGitHubConnect(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true}, GitHubConnectDraft{BoxName: "work", NeedsDeviceFlow: true})
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	got := output.String()
	for _, phrase := range []string{"GitHub access", "GitHub App", "Git SSH keys: read and write", "cannot read your repositories", "Authorize Schooner with GitHub?"} {
		if !strings.Contains(got, phrase) {
			t.Fatalf("output %q does not contain %q", got, phrase)
		}
	}
}

func TestConfirmGitHubConnectReuseSkipsAppSermon(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmGitHubConnect(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true}, GitHubConnectDraft{BoxName: "work", AccountLogin: "octocat"})
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	got := output.String()
	if !strings.Contains(got, "already authorized") || !strings.Contains(got, "octocat") || strings.Contains(got, "GitHub App") {
		t.Fatalf("output = %q", got)
	}
}

func TestConfirmGitHubCloneRecoveryNamesTheRepository(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmGitHubCloneRecovery(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true}, "work", "owner/repo")
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	got := output.String()
	if !strings.Contains(got, "owner/repo") || !strings.Contains(got, "already tried") || !strings.Contains(got, "Create a dedicated Box key?") {
		t.Fatalf("output = %q", got)
	}
}

func TestConfirmGitHubDisconnectWarnsWhenRemovingTheLastBox(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmGitHubDisconnect(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true}, GitHubDisconnectDraft{BoxName: "work", AccountLogin: "octocat", KeyTitle: "Schooner / work", LastBox: true})
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	got := output.String()
	if !strings.Contains(got, "last connected Box") || !strings.Contains(got, "Schooner / work") {
		t.Fatalf("output = %q", got)
	}
}

func TestConfirmDatabaseDestroyStatesExternalResourcesAreUnchanged(t *testing.T) {
	var output bytes.Buffer
	confirmed, err := ConfirmDatabaseDestroy(t.Context(), Options{Input: strings.NewReader("y\n"), Output: &output, Accessible: true})
	if err != nil || !confirmed {
		t.Fatalf("confirmed = %v, err = %v", confirmed, err)
	}
	for _, phrase := range []string{"DigitalOcean resources", "Credential-store entries", "unchanged"} {
		if !strings.Contains(output.String(), phrase) {
			t.Fatalf("output %q does not contain %q", output.String(), phrase)
		}
	}
}
