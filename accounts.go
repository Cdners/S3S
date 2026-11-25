package main

import (
	"context"
	"crypto/rand"

	"github.com/Cdners/S3S/internal/core"
	"github.com/Cdners/S3S/internal/s3client"
	"github.com/Cdners/S3S/internal/vault"
)

// Scope includes the account ID, provider, region and endpoint. Renaming a
// profile does not change where its credentials are permitted to travel.
func (w *Workspace) currentProfile(ctx context.Context, expected core.Connection) (core.Connection, error) {
	if w.store == nil {
		return core.Connection{}, core.NewError(w.unavailable())
	}
	current, err := w.store.Connection(ctx, expected.ID)
	if err != nil {
		return core.Connection{}, err
	}
	if vault.ScopeOf(current) != vault.ScopeOf(expected) {
		return core.Connection{}, core.NewError("error.connectionChanged")
	}
	return current, nil
}

func (w *Workspace) validateCredentials(c core.Connection, input vault.Credentials) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if c.Provider == core.AWS || c.Provider == core.R2 {
		// Construction validates the adapter's signing origin locally.
		_, err := s3client.NewWithTransport(c, sdkCredentials(input), w.storageTransport)
		return err
	}
	// Other known providers can store a scoped credential before their cloud
	// adapter is available. Connection.Validate rejects unknown providers.
	return c.Validate()
}

// SaveAccount updates public metadata and optionally registers a write-only
// credential replacement. Data is the committed profile, even if the separate
// keyring write failed. Retrying with that ID edits the same SQLite record.
func (w *Workspace) SaveAccount(ctx context.Context, previous *core.Connection, changes core.Connection, input *vault.Credentials) response[*core.Connection] {
	if w.store == nil {
		return failure[*core.Connection](nil, w.unavailable())
	}
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	if previous == nil {
		if changes.ID != "" {
			return failure[*core.Connection](nil, core.InvalidConnection)
		}
		changes.ID = rand.Text()
	} else {
		if previous.ID == "" || changes.ID != previous.ID {
			return failure[*core.Connection](nil, "error.connectionChanged")
		}
		current, err := w.store.Connection(ctx, previous.ID)
		if err != nil {
			return failure[*core.Connection](err, core.Storage)
		}
		if current != *previous {
			return failure[*core.Connection](nil, "error.connectionChanged")
		}
	}
	replace := input != nil && !input.Empty()
	if replace {
		if err := w.validateCredentials(changes, *input); err != nil {
			return failure[*core.Connection](err, "error.invalidCredentials")
		}
	}
	if err := w.store.SaveConnection(ctx, changes); err != nil {
		return failure[*core.Connection](err, "error.storageWrite")
	}
	if replace {
		if err := (vault.Vault{}).Save(changes, *input); err != nil {
			result := failure[*core.Connection](err, "error.keyringUnavailable")
			result.Data = &changes
			return result
		}
	}
	return response[*core.Connection]{Data: &changes}
}

// SetCredentials supports callers that provision keys for an existing profile.
func (w *Workspace) SetCredentials(ctx context.Context, expected core.Connection, input vault.Credentials) response[bool] {
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	c, err := w.currentProfile(ctx, expected)
	if err == nil {
		err = w.validateCredentials(c, input)
	}
	if err == nil {
		err = (vault.Vault{}).Save(c, input)
	}
	if err != nil {
		return failure[bool](err, "error.keyringUnavailable")
	}
	return response[bool]{Data: true}
}

func (w *Workspace) ForgetCredentials(ctx context.Context, expected core.Connection) response[bool] {
	w.profilesMu.Lock()
	defer w.profilesMu.Unlock()
	c, err := w.currentProfile(ctx, expected)
	if err == nil {
		err = (vault.Vault{}).Forget(c)
	}
	if err != nil {
		return failure[bool](err, "error.keyringUnavailable")
	}
	return response[bool]{Data: true}
}
