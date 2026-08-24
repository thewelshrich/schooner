package digitalocean

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/provider"
	sshRuntime "github.com/thewelshrich/schooner/internal/runtime/ssh"
)

func TestLiveCreateSSHAndDestroy(t *testing.T) {
	if os.Getenv("SCHOONER_LIVE_DIGITALOCEAN") != "1" {
		t.Skip("set SCHOONER_LIVE_DIGITALOCEAN=1 to run the billable live test")
	}
	token := os.Getenv("DIGITALOCEAN_TOKEN")
	if token == "" {
		t.Fatal("DIGITALOCEAN_TOKEN is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	cloud := New()
	if _, err := cloud.Verify(ctx, token); err != nil {
		t.Fatal(err)
	}
	catalog, err := cloud.Catalog(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	region, size, image, ok := cheapestCompatible(catalog)
	if !ok {
		t.Fatal("DigitalOcean returned no compatible Ubuntu configuration")
	}
	identity, err := sshRuntime.EnsureIdentity(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	correlation := fmt.Sprintf("live-%d", time.Now().Unix())
	request := provider.ProvisionRequest{Name: "schooner-live-" + fmt.Sprint(time.Now().Unix()), CorrelationID: correlation, Region: region, Size: size, Image: image, IPv6: true, ControlPublicKey: identity.PublicKey}
	machine, err := cloud.Provision(ctx, token, request)
	if err != nil && box.ErrorCode(err) == "outcome_unknown" {
		machine, err = cloud.Provision(ctx, token, request)
	}
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.ResourceRef{Provider: provider.DigitalOcean, ResourceID: machine.ResourceID, CorrelationID: correlation, Profile: "digitalocean/live-test"}
	destroyed := false
	defer func() {
		if !destroyed {
			if cleanupErr := cloud.Destroy(context.Background(), token, ref); cleanupErr != nil {
				t.Errorf("live cleanup failed: %v", cleanupErr)
			}
		}
	}()
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	deadline := time.Now().Add(5 * time.Minute)
	for {
		command := exec.CommandContext(ctx, "ssh", "-i", identity.PrivateKey, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile="+knownHosts, "root@"+machine.PublicIPv4, "true")
		if command.Run() == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Droplet did not become reachable over SSH")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	if err = cloud.Destroy(ctx, token, ref); err != nil {
		t.Fatal(err)
	}
	destroyed = true
}

func cheapestCompatible(catalog provider.Catalog) (string, string, string, bool) {
	for _, size := range catalog.Sizes {
		for _, region := range size.Regions {
			for _, image := range catalog.Images {
				if len(image.Regions) == 0 || contains(image.Regions, region) {
					return region, size.ID, image.ID, true
				}
			}
		}
	}
	return "", "", "", false
}
