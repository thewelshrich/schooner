package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/thewelshrich/schooner/internal/box"
	uitheme "github.com/thewelshrich/schooner/internal/ui/theme"
)

func TestWriteAddResultHumanReadySummary(t *testing.T) {
	var out bytes.Buffer
	result := box.AddResult{
		Box: box.Record{
			Name:               "newtest",
			SSHDestination:     "root@209.97.139.0",
			ProjectRoot:        "/root/schooner",
			Provider:           "digitalocean",
			ProviderResourceID: "594799357",
		},
		Capabilities: box.Capabilities{
			OSID:         "ubuntu",
			OSVersion:    "26.04",
			Architecture: "amd64",
			Git:          box.Tool{Version: "git version 2.53.0"},
			Tmux:         box.Tool{Version: "tmux 3.6"},
		},
		Installed: []string{"git"},
	}
	if err := writeAddResult(&out, "human", result, uitheme.New(uitheme.Auto, false)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"\n✓ newtest is ready\n",
		"  Provider      digitalocean (594799357)\n",
		"  SSH           root@209.97.139.0\n",
		"  Project root  /root/schooner\n",
		"  OS            Ubuntu 26.04 (amd64)\n",
		"  Git           git version 2.53.0\n",
		"  tmux          tmux 3.6\n",
		"  Installed     git\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Added box") {
		t.Fatalf("legacy heading still present: %q", got)
	}
}

func TestWriteAddResultJSONUnchangedShape(t *testing.T) {
	var out bytes.Buffer
	result := box.AddResult{
		Box:          box.Record{ID: "box-id", Name: "work", Acquisition: "adopted", SSHDestination: "work-host", RemoteIdentity: "remote", ProjectRoot: "/home/alice/schooner"},
		Capabilities: box.Capabilities{OSID: "ubuntu", OSVersion: "24.04", Architecture: "amd64", Git: box.Tool{Available: true, Version: "git version 2.43.0"}, Tmux: box.Tool{Available: true, Version: "tmux 3.4"}},
	}
	if err := writeAddResult(&out, "json", result, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"1"`) || !strings.Contains(out.String(), `"name":"work"`) {
		t.Fatalf("json = %s", out.String())
	}
}
