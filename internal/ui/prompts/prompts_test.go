package prompts

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
)

func TestAddAccessibleReviewAndConfirm(t *testing.T) {
	var output bytes.Buffer
	draft, confirmed, err := Add(t.Context(), Options{Input: &oneByteReader{reader: strings.NewReader("1\ny\n")}, Output: &output, Accessible: true}, AddDraft{Name: "work", SSHDestination: "work-host", ProjectRoot: "~/schooner"}, true, true, true, false)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !confirmed || draft.Name != "work" {
		t.Fatalf("draft = %+v, confirmed = %v", draft, confirmed)
	}
	if !strings.Contains(output.String(), "Existing SSH") || !strings.Contains(output.String(), "Review") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestAddAccessibleDecline(t *testing.T) {
	var output bytes.Buffer
	_, confirmed, err := Add(t.Context(), Options{Input: &oneByteReader{reader: strings.NewReader("1\nn\n")}, Output: &output, Accessible: true}, AddDraft{Name: "work", SSHDestination: "work-host", ProjectRoot: "~/schooner"}, true, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed {
		t.Fatal("declined form was confirmed")
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
}
