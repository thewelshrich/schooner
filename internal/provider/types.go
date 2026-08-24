// Package provider defines provider-neutral infrastructure acquisition values.
package provider

import (
	"context"
	"fmt"
)

type ID string

const DigitalOcean ID = "digitalocean"

type CredentialProfileRef string

func ProfileRef(provider ID, name string) CredentialProfileRef {
	return CredentialProfileRef(string(provider) + "/" + name)
}

type ResourceRef struct {
	Provider      ID
	ResourceID    string
	CorrelationID string
	Profile       CredentialProfileRef
}

type Region struct {
	ID   string
	Name string
}

type Price struct {
	Monthly  float64
	Currency string
}

type Size struct {
	ID       string
	Name     string
	MemoryMB int
	VCPUs    int
	DiskGB   int
	Regions  []string
	Price    Price
}

type Image struct {
	ID      string
	Name    string
	Regions []string
}

type Network struct {
	ID     string
	Name   string
	Region string
}

type AccessKey struct {
	ID          string
	Name        string
	Fingerprint string
}

// PublicKey is public SSH material selected from the user's local machine.
// Private key paths and contents never cross the provider boundary.
type PublicKey struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

type Catalog struct {
	Regions    []Region
	Sizes      []Size
	Images     []Image
	Networks   []Network
	AccessKeys []AccessKey
}

type ProvisionRequest struct {
	Name             string
	CorrelationID    string
	Region           string
	Size             string
	Image            string
	NetworkID        string
	AccessKeyIDs     []string
	LocalPublicKeys  []PublicKey
	AutomaticBackups bool
	IPv6             bool
	ControlPublicKey string
	KnownResourceID  string
}

type ProvisionedMachine struct {
	ResourceID  string
	PublicIPv4  string
	SSHUsername string
	Warning     string
}

type Account struct {
	ExternalID string
	Name       string
	Email      string
}

type Resource struct {
	ID            string
	Name          string
	CorrelationID string
}

// Cloud is the acquisition-owned seam implemented by a provider adapter.
// Implementations must reconcile Provision by CorrelationID before creating.
type Cloud interface {
	Identify(context.Context, string) (Account, error)
	Verify(context.Context, string) (Account, error)
	Catalog(context.Context, string) (Catalog, error)
	Provision(context.Context, string, ProvisionRequest) (ProvisionedMachine, error)
	Inspect(context.Context, string, ResourceRef) (Resource, error)
	Destroy(context.Context, string, ResourceRef) error
}

func (r ResourceRef) Validate() error {
	if r.Provider == "" || r.ResourceID == "" || r.CorrelationID == "" || r.Profile == "" {
		return fmt.Errorf("provider resource reference is incomplete")
	}
	return nil
}
