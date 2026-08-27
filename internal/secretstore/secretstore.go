// Package secretstore keeps secret values outside Schooner's ordinary local
// inventory. Callers choose a service namespace and persist only opaque keys.
package secretstore

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// Store is the caller-owned seam used by modules that persist opaque secret
// references in ordinary state.
type Store interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

// Keyring stores values in the operating-system credential store under one
// application-specific service namespace.
type Keyring struct{ Service string }

func (k Keyring) Get(key string) (string, error) {
	value, err := keyring.Get(k.Service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return value, err
}

func (k Keyring) Set(key, value string) error { return keyring.Set(k.Service, key, value) }

func (k Keyring) Delete(key string) error {
	err := keyring.Delete(k.Service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
