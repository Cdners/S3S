// Package vault stores opt-in credentials in the platform keyring. SQLite stores
// only public connection metadata. The bridge never provides a credential getter.
package vault

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Cdners/S3S/internal/core"
	"github.com/zalando/go-keyring"
)

const service = "io.github.cdners.S3S"

type Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
}

// Empty distinguishes leaving write-only fields blank from a partial replacement.
func (c Credentials) Empty() bool {
	return strings.TrimSpace(c.AccessKeyID) == "" && strings.TrimSpace(c.SecretAccessKey) == "" && strings.TrimSpace(c.SessionToken) == ""
}

func (c Credentials) Validate() error {
	if strings.TrimSpace(c.AccessKeyID) == "" || strings.TrimSpace(c.SecretAccessKey) == "" {
		return core.NewError("error.invalidCredentials")
	}
	return nil
}

type Scope struct {
	ID       string
	Provider core.Provider
	Region   string
	Endpoint string
}

func ScopeOf(c core.Connection) Scope {
	return Scope{ID: c.ID, Provider: c.Provider, Region: c.Region, Endpoint: c.Endpoint}
}

type entry struct {
	Scope       Scope
	Credentials Credentials
}

// One item per connection. Replacing keys is atomic at the keyring item level.
// Retargeting a profile cannot forward keys saved for its previous destination.
type Vault struct{}

func (Vault) Save(c core.Connection, credentials Credentials) error {
	if err := credentials.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(entry{ScopeOf(c), credentials})
	if err != nil {
		return core.NewError("error.invalidCredentials")
	}
	err = keyring.Set(service, c.ID, string(payload))
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return core.NewError("error.invalidCredentials")
	}
	if err != nil {
		return core.NewError("error.keyringUnavailable")
	}
	return nil
}

func (Vault) Load(c core.Connection) (Credentials, error) {
	payload, err := keyring.Get(service, c.ID)
	if errors.Is(err, keyring.ErrNotFound) {
		return Credentials{}, core.NewError("error.credentialsMissing")
	}
	if err != nil {
		return Credentials{}, core.NewError("error.keyringUnavailable")
	}
	var item entry
	if json.Unmarshal([]byte(payload), &item) != nil || item.Credentials.Validate() != nil {
		return Credentials{}, core.NewError("error.keyringUnavailable")
	}
	if item.Scope != ScopeOf(c) {
		return Credentials{}, core.NewError("error.credentialsMissing")
	}
	return item.Credentials, nil
}

func (Vault) Forget(c core.Connection) error {
	// The bridge guards against a stale editor by checking the public profile.
	// Explicit removal also clears obsolete credentials after a profile edit.
	err := keyring.Delete(service, c.ID)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return core.NewError("error.keyringUnavailable")
	}
	return nil
}
