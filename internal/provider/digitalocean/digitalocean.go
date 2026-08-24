// Package digitalocean implements provider infrastructure acquisition using
// DigitalOcean's official Go client.
package digitalocean

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"
	"github.com/thewelshrich/schooner/internal/box"
	"github.com/thewelshrich/schooner/internal/provider"
	"golang.org/x/oauth2"
)

const perPage = 200

type Cloud struct {
	baseURL string
	timeout time.Duration
	wait    func(context.Context, time.Duration) error
}

func New() *Cloud {
	return &Cloud{timeout: 25 * time.Second, wait: waitContext}
}

func (c *Cloud) client(token string) *godo.Client {
	source := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: strings.TrimSpace(token)})
	httpClient := oauth2.NewClient(context.Background(), source)
	httpClient.Timeout = c.timeout
	client := godo.NewClient(httpClient)
	client.UserAgent = "Schooner CLI DigitalOcean provider"
	if c.baseURL != "" {
		client.BaseURL, _ = client.BaseURL.Parse(c.baseURL)
	}
	return client
}

func (c *Cloud) Identify(ctx context.Context, token string) (provider.Account, error) {
	client := c.client(token)
	account, _, err := client.Account.Get(ctx)
	if err != nil {
		return provider.Account{}, classify(err, "verify DigitalOcean credential")
	}
	externalID, name := account.UUID, account.Name
	if account.Team != nil {
		externalID, name = account.Team.UUID, account.Team.Name
	}
	if externalID == "" {
		return provider.Account{}, box.NewError("connection_failed", "DigitalOcean account response omitted its stable team identity", nil)
	}
	return provider.Account{ExternalID: externalID, Name: name, Email: account.Email}, nil
}

func (c *Cloud) Verify(ctx context.Context, token string) (provider.Account, error) {
	account, err := c.Identify(ctx, token)
	if err != nil {
		return provider.Account{}, err
	}
	if _, err = c.Catalog(ctx, token); err != nil {
		return provider.Account{}, err
	}
	client := c.client(token)
	probeName := fmt.Sprintf("schooner-permission-probe-%d", time.Now().UnixNano())
	probeKey, err := uniqueProbePublicKey()
	if err != nil {
		return provider.Account{}, box.NewError("internal", "could not generate DigitalOcean permission probe", err)
	}
	probe, _, err := client.Keys.Create(ctx, &godo.KeyCreateRequest{Name: probeName, PublicKey: probeKey})
	if err != nil {
		return provider.Account{}, classify(err, "verify DigitalOcean SSH-key permissions")
	}
	if _, err = client.Keys.DeleteByID(ctx, probe.ID); err != nil {
		return provider.Account{}, box.NewError("outcome_unknown", fmt.Sprintf("DigitalOcean created permission probe key %q but its cleanup could not be confirmed; remove it in DigitalOcean before retrying", probeName), nil)
	}
	return account, nil
}

func (c *Cloud) Catalog(ctx context.Context, token string) (provider.Catalog, error) {
	client := c.client(token)
	regions, err := allRegions(ctx, client)
	if err != nil {
		return provider.Catalog{}, classify(err, "list DigitalOcean regions")
	}
	sizes, err := allSizes(ctx, client)
	if err != nil {
		return provider.Catalog{}, classify(err, "list DigitalOcean sizes")
	}
	images, err := allImages(ctx, client)
	if err != nil {
		return provider.Catalog{}, classify(err, "list DigitalOcean images")
	}
	vpcs, err := allVPCs(ctx, client)
	if err != nil {
		return provider.Catalog{}, classify(err, "list DigitalOcean VPCs")
	}
	keys, err := allKeys(ctx, client)
	if err != nil {
		return provider.Catalog{}, classify(err, "list DigitalOcean SSH keys")
	}
	result := provider.Catalog{}
	for _, region := range regions {
		if region.Available {
			result.Regions = append(result.Regions, provider.Region{ID: region.Slug, Name: region.Name})
		}
	}
	for _, size := range sizes {
		if size.Available && size.GPUInfo == nil {
			result.Sizes = append(result.Sizes, provider.Size{ID: size.Slug, Name: first(size.Description, size.Slug), MemoryMB: size.Memory, VCPUs: size.Vcpus, DiskGB: size.Disk, Regions: append([]string(nil), size.Regions...), Price: provider.Price{Monthly: size.PriceMonthly, Currency: "USD"}})
		}
	}
	for _, image := range images {
		if image.Distribution == "Ubuntu" && supportedUbuntu(image.Slug) {
			result.Images = append(result.Images, provider.Image{ID: image.Slug, Name: image.Name, Regions: append([]string(nil), image.Regions...)})
		}
	}
	for _, vpc := range vpcs {
		result.Networks = append(result.Networks, provider.Network{ID: vpc.ID, Name: vpc.Name, Region: vpc.RegionSlug})
	}
	for _, key := range keys {
		result.AccessKeys = append(result.AccessKeys, provider.AccessKey{ID: strconv.Itoa(key.ID), Name: key.Name, Fingerprint: key.Fingerprint})
	}
	sort.Slice(result.Regions, func(i, j int) bool { return result.Regions[i].ID < result.Regions[j].ID })
	sort.Slice(result.Sizes, func(i, j int) bool {
		if result.Sizes[i].Price.Monthly == result.Sizes[j].Price.Monthly {
			return result.Sizes[i].ID < result.Sizes[j].ID
		}
		return result.Sizes[i].Price.Monthly < result.Sizes[j].Price.Monthly
	})
	sort.Slice(result.Images, func(i, j int) bool { return result.Images[i].ID > result.Images[j].ID })
	return result, nil
}

func (c *Cloud) Provision(ctx context.Context, token string, request provider.ProvisionRequest) (provider.ProvisionedMachine, error) {
	if err := c.validateSelection(ctx, token, request); err != nil {
		return provider.ProvisionedMachine{}, err
	}
	client := c.client(token)
	tag := correlationTag(request.CorrelationID)
	droplet, err := oneCorrelatedDroplet(ctx, client, tag)
	if err != nil {
		return provider.ProvisionedMachine{}, err
	}
	if request.KnownResourceID != "" {
		known, parseErr := strconv.Atoi(request.KnownResourceID)
		if parseErr != nil {
			return provider.ProvisionedMachine{}, box.NewError("conflict", "recorded DigitalOcean Droplet ID is invalid", parseErr)
		}
		if droplet != nil && droplet.ID != known {
			return provider.ProvisionedMachine{}, box.NewError("conflict", "recorded and correlated DigitalOcean Droplets differ", nil)
		}
		if droplet == nil {
			droplet, _, err = client.Droplets.Get(ctx, known)
			if notFound(err) {
				// The recorded resource was removed outside Schooner. Because the
				// correlation lookup also found nothing, recreating it cannot
				// duplicate the original operation.
				droplet = nil
			} else if err != nil {
				return provider.ProvisionedMachine{}, classify(err, "inspect recorded DigitalOcean Droplet")
			}
		}
	}
	if droplet != nil && !contains(droplet.Tags, tag) {
		return provider.ProvisionedMachine{}, box.NewError("conflict", "recorded DigitalOcean Droplet no longer has its Schooner correlation tag", nil)
	}

	warning := ""
	if droplet == nil {
		controlKey, owned, ensureErr := ensureControlKey(ctx, client, request.CorrelationID, request.ControlPublicKey)
		if ensureErr != nil {
			return provider.ProvisionedMachine{}, ensureErr
		}
		managedKeys := []managedKey{{Key: controlKey, Owned: owned}}
		sshKeys := []godo.DropletCreateSSHKey{{ID: controlKey.ID}}
		selectedIDs := map[int]bool{controlKey.ID: true}
		for index, localKey := range request.LocalPublicKeys {
			key, localOwned, localErr := ensureNamedKey(ctx, client, localKeyName(request.CorrelationID, index), localKey.PublicKey)
			if localErr != nil {
				return provider.ProvisionedMachine{}, localErr
			}
			managedKeys = append(managedKeys, managedKey{Key: key, Owned: localOwned})
			if !selectedIDs[key.ID] {
				sshKeys = append(sshKeys, godo.DropletCreateSSHKey{ID: key.ID})
				selectedIDs[key.ID] = true
			}
		}
		for _, value := range request.AccessKeyIDs {
			id, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				return provider.ProvisionedMachine{}, box.NewError("invalid_input", "DigitalOcean SSH key selection is invalid", parseErr)
			}
			if !selectedIDs[id] {
				sshKeys = append(sshKeys, godo.DropletCreateSSHKey{ID: id})
				selectedIDs[id] = true
			}
		}
		created, _, createErr := client.Droplets.Create(ctx, &godo.DropletCreateRequest{Name: request.Name, Region: request.Region, Size: request.Size, Image: godo.DropletCreateImage{Slug: request.Image}, SSHKeys: sshKeys, Backups: request.AutomaticBackups, IPv6: request.IPv6, Monitoring: true, Tags: []string{"schooner", tag}, VPCUUID: request.NetworkID})
		if createErr != nil {
			created, reconcileErr := oneCorrelatedDroplet(ctx, client, tag)
			if reconcileErr != nil || created == nil {
				if definiteRejection(createErr) {
					_ = cleanupManagedKeys(ctx, client, managedKeys)
				}
				return provider.ProvisionedMachine{}, classifyCreate(createErr)
			}
			droplet = created
		} else {
			droplet = created
		}
		if cleanupErr := cleanupManagedKeys(ctx, client, managedKeys); cleanupErr != nil {
			warning = "Droplet created, but one or more temporary DigitalOcean SSH key records could not be removed"
		}
	} else if cleanupErr := cleanupOperationKeys(ctx, client, request); cleanupErr != nil {
		warning = "Droplet reconciled, but one or more temporary DigitalOcean SSH key records could not be removed"
	}
	address, err := c.waitForPublicIPv4(ctx, client, droplet.ID)
	if err != nil {
		return provider.ProvisionedMachine{}, err
	}
	return provider.ProvisionedMachine{ResourceID: strconv.Itoa(droplet.ID), PublicIPv4: address, SSHUsername: "root", Warning: warning}, nil
}

func (c *Cloud) Inspect(ctx context.Context, token string, ref provider.ResourceRef) (provider.Resource, error) {
	id, err := strconv.Atoi(ref.ResourceID)
	if err != nil {
		return provider.Resource{}, box.NewError("conflict", "recorded DigitalOcean Droplet ID is invalid", err)
	}
	droplet, _, err := c.client(token).Droplets.Get(ctx, id)
	if err != nil {
		return provider.Resource{}, classify(err, "inspect DigitalOcean Droplet")
	}
	tag := correlationTag(ref.CorrelationID)
	if !contains(droplet.Tags, tag) {
		return provider.Resource{}, box.NewError("conflict", "DigitalOcean Droplet no longer has its Schooner correlation tag; destroy it manually or restore the tag", nil)
	}
	return provider.Resource{ID: strconv.Itoa(droplet.ID), Name: droplet.Name, CorrelationID: ref.CorrelationID}, nil
}

func (c *Cloud) Destroy(ctx context.Context, token string, ref provider.ResourceRef) error {
	if _, err := c.Inspect(ctx, token, ref); err != nil {
		return err
	}
	id, _ := strconv.Atoi(ref.ResourceID)
	client := c.client(token)
	if _, err := client.Droplets.Delete(ctx, id); err != nil && !notFound(err) {
		if definiteRejection(err) {
			return classify(err, "destroy DigitalOcean Droplet")
		}
		return box.NewError("outcome_unknown", "DigitalOcean Droplet deletion may have taken effect; retry destroy to reconcile it", err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		_, _, err := client.Droplets.Get(ctx, id)
		if notFound(err) {
			return nil
		}
		if err != nil {
			return box.NewError("outcome_unknown", "DigitalOcean Droplet deletion could not be confirmed; retry destroy to reconcile it", nil)
		}
		if err = c.wait(ctx, time.Duration(attempt+1)*time.Second); err != nil {
			return err
		}
	}
	return box.NewError("outcome_unknown", "DigitalOcean accepted deletion but the Droplet still exists; retry destroy shortly", nil)
}

func (c *Cloud) validateSelection(ctx context.Context, token string, request provider.ProvisionRequest) error {
	catalog, err := c.Catalog(ctx, token)
	if err != nil {
		return err
	}
	if !sliceContains(catalog.Regions, request.Region, func(v provider.Region) string { return v.ID }) {
		return box.NewError("invalid_input", "selected DigitalOcean region is unavailable", nil)
	}
	sizeOK := false
	for _, size := range catalog.Sizes {
		if size.ID == request.Size && contains(size.Regions, request.Region) {
			sizeOK = true
		}
	}
	if !sizeOK {
		return box.NewError("invalid_input", "selected DigitalOcean size is unavailable in that region", nil)
	}
	imageOK := false
	for _, image := range catalog.Images {
		if image.ID == request.Image && (len(image.Regions) == 0 || contains(image.Regions, request.Region)) {
			imageOK = true
		}
	}
	if !imageOK {
		return box.NewError("invalid_input", "selected Ubuntu image is unavailable in that region", nil)
	}
	if request.NetworkID != "" {
		found := false
		for _, network := range catalog.Networks {
			found = found || (network.ID == request.NetworkID && network.Region == request.Region)
		}
		if !found {
			return box.NewError("invalid_input", "selected DigitalOcean VPC is unavailable in that region", nil)
		}
	}
	availableKeys := map[string]bool{}
	for _, key := range catalog.AccessKeys {
		availableKeys[key.ID] = true
	}
	if len(request.AccessKeyIDs)+len(request.LocalPublicKeys) > 15 {
		return box.NewError("invalid_input", "select at most 15 additional SSH keys", nil)
	}
	selected := map[string]bool{}
	for _, key := range request.AccessKeyIDs {
		if !availableKeys[key] {
			return box.NewError("invalid_input", "a selected DigitalOcean account SSH key is no longer available", nil)
		}
		if selected[key] {
			return box.NewError("invalid_input", "select each DigitalOcean account SSH key at most once", nil)
		}
		selected[key] = true
	}
	localFingerprints := map[string]bool{}
	for _, key := range request.LocalPublicKeys {
		if key.Name == "" || key.Fingerprint == "" || len(strings.Fields(key.PublicKey)) < 2 {
			return box.NewError("invalid_input", "a selected local SSH public key is invalid", nil)
		}
		if localFingerprints[key.Fingerprint] {
			return box.NewError("invalid_input", "select each local SSH public key at most once", nil)
		}
		localFingerprints[key.Fingerprint] = true
	}
	return nil
}

func (c *Cloud) waitForPublicIPv4(ctx context.Context, client *godo.Client, id int) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		droplet, _, err := client.Droplets.Get(ctx, id)
		if err != nil {
			return "", classify(err, "wait for DigitalOcean Droplet address")
		}
		if address, _ := droplet.PublicIPv4(); address != "" {
			return address, nil
		}
		if err = c.wait(ctx, 2*time.Second); err != nil {
			return "", err
		}
	}
	return "", box.NewError("outcome_unknown", "DigitalOcean Droplet exists but its public IPv4 address is not available yet; retry box add", nil)
}

func oneCorrelatedDroplet(ctx context.Context, client *godo.Client, tag string) (*godo.Droplet, error) {
	droplets, _, err := client.Droplets.ListByTag(ctx, tag, &godo.ListOptions{PerPage: perPage})
	if err != nil {
		return nil, classify(err, "reconcile DigitalOcean Droplet")
	}
	if len(droplets) > 1 {
		return nil, box.NewError("conflict", "multiple DigitalOcean Droplets match this Schooner operation", nil)
	}
	if len(droplets) == 0 {
		return nil, nil
	}
	return &droplets[0], nil
}

func ensureControlKey(ctx context.Context, client *godo.Client, correlationID, publicKey string) (godo.Key, bool, error) {
	return ensureNamedKey(ctx, client, "schooner-"+correlationID, publicKey)
}

func ensureNamedKey(ctx context.Context, client *godo.Client, name, publicKey string) (godo.Key, bool, error) {
	keys, err := allKeys(ctx, client)
	if err != nil {
		return godo.Key{}, false, classify(err, "inspect DigitalOcean SSH keys")
	}
	var matches []godo.Key
	for _, key := range keys {
		if key.Name == name && !samePublicKey(key.PublicKey, publicKey) {
			return godo.Key{}, false, box.NewError("conflict", "DigitalOcean contains a different SSH key with this operation name", nil)
		}
		if samePublicKey(key.PublicKey, publicKey) {
			matches = append(matches, key)
		}
	}
	if len(matches) > 1 {
		return godo.Key{}, false, box.NewError("conflict", "multiple DigitalOcean SSH keys match this operation", nil)
	}
	if len(matches) == 1 {
		return matches[0], matches[0].Name == name, nil
	}
	created, _, err := client.Keys.Create(ctx, &godo.KeyCreateRequest{Name: name, PublicKey: publicKey})
	if err != nil {
		return godo.Key{}, false, classify(err, "create temporary DigitalOcean SSH key")
	}
	return *created, true, nil
}

type managedKey struct {
	Key   godo.Key
	Owned bool
}

func localKeyName(correlationID string, index int) string {
	return fmt.Sprintf("schooner-%s-local-%d", correlationID, index+1)
}

func cleanupManagedKeys(ctx context.Context, client *godo.Client, keys []managedKey) error {
	var cleanupErr error
	for _, key := range keys {
		if !key.Owned {
			continue
		}
		if _, err := client.Keys.DeleteByID(ctx, key.Key.ID); err != nil && !notFound(err) {
			cleanupErr = err
		}
	}
	return cleanupErr
}

func cleanupOperationKeys(ctx context.Context, client *godo.Client, request provider.ProvisionRequest) error {
	keys, err := allKeys(ctx, client)
	if err != nil {
		return err
	}
	expected := map[string]string{"schooner-" + request.CorrelationID: request.ControlPublicKey}
	for index, key := range request.LocalPublicKeys {
		expected[localKeyName(request.CorrelationID, index)] = key.PublicKey
	}
	var cleanupErr error
	for _, key := range keys {
		publicKey, owned := expected[key.Name]
		if !owned || !samePublicKey(key.PublicKey, publicKey) {
			continue
		}
		if _, deleteErr := client.Keys.DeleteByID(ctx, key.ID); deleteErr != nil && !notFound(deleteErr) {
			cleanupErr = deleteErr
		}
	}
	return cleanupErr
}

func allRegions(ctx context.Context, client *godo.Client) ([]godo.Region, error) {
	var result []godo.Region
	for page := 1; ; page++ {
		values, _, err := client.Regions.List(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
		if len(values) < perPage {
			return result, nil
		}
	}
}
func allSizes(ctx context.Context, client *godo.Client) ([]godo.Size, error) {
	var result []godo.Size
	for page := 1; ; page++ {
		values, _, err := client.Sizes.List(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
		if len(values) < perPage {
			return result, nil
		}
	}
}
func allImages(ctx context.Context, client *godo.Client) ([]godo.Image, error) {
	var result []godo.Image
	for page := 1; ; page++ {
		values, _, err := client.Images.ListDistribution(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
		if len(values) < perPage {
			return result, nil
		}
	}
}
func allVPCs(ctx context.Context, client *godo.Client) ([]*godo.VPC, error) {
	var result []*godo.VPC
	for page := 1; ; page++ {
		values, _, err := client.VPCs.List(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
		if len(values) < perPage {
			return result, nil
		}
	}
}
func allKeys(ctx context.Context, client *godo.Client) ([]godo.Key, error) {
	var result []godo.Key
	for page := 1; ; page++ {
		values, _, err := client.Keys.List(ctx, &godo.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return nil, err
		}
		result = append(result, values...)
		if len(values) < perPage {
			return result, nil
		}
	}
}

func classify(err error, action string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var response *godo.ErrorResponse
	if errors.As(err, &response) && response.Response != nil {
		switch response.Response.StatusCode {
		case http.StatusUnauthorized:
			return box.NewError("authentication_required", "DigitalOcean rejected the personal access token", err)
		case http.StatusForbidden:
			return box.NewError("permission_denied", "DigitalOcean token lacks a required permission", err)
		case http.StatusNotFound:
			return box.NewError("not_found", "DigitalOcean resource was not found", err)
		case http.StatusConflict, http.StatusUnprocessableEntity:
			return box.NewError("conflict", "DigitalOcean rejected conflicting resource details", err)
		}
	}
	return box.NewError("connection_failed", action+" failed", err)
}

func notFound(err error) bool {
	var response *godo.ErrorResponse
	return errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == http.StatusNotFound
}
func definiteRejection(err error) bool {
	var response *godo.ErrorResponse
	return errors.As(err, &response) && response.Response != nil && response.Response.StatusCode >= 400 && response.Response.StatusCode < 500 && response.Response.StatusCode != http.StatusTooManyRequests
}
func classifyCreate(err error) error {
	if definiteRejection(err) {
		return classify(err, "create DigitalOcean Droplet")
	}
	return box.NewError("outcome_unknown", "DigitalOcean Droplet creation could not be confirmed; retry box add to reconcile by its Schooner tag", err)
}
func supportedUbuntu(slug string) bool {
	return strings.Contains(slug, "24-04") || strings.Contains(slug, "26-04")
}
func correlationTag(id string) string { return "schooner-op-" + id }
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func sliceContains[T any](values []T, target string, key func(T) string) bool {
	for _, value := range values {
		if key(value) == target {
			return true
		}
	}
	return false
}
func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func samePublicKey(a, b string) bool {
	aa, bb := strings.Fields(a), strings.Fields(b)
	return len(aa) >= 2 && len(bb) >= 2 && aa[0] == bb[0] && aa[1] == bb[1]
}
func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func uniqueProbePublicKey() (string, error) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	algorithm := []byte("ssh-ed25519")
	wire := make([]byte, 4+len(algorithm)+4+len(public))
	binary.BigEndian.PutUint32(wire[:4], uint32(len(algorithm)))
	copy(wire[4:], algorithm)
	offset := 4 + len(algorithm)
	binary.BigEndian.PutUint32(wire[offset:offset+4], uint32(len(public)))
	copy(wire[offset+4:], public)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire) + " schooner-permission-probe", nil
}
